package gemini

import (
	"context"
	"fmt"

	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	"github.com/kumori-sh/spacetrk/src/core/ports"
	"google.golang.org/genai"
)

const defaultSystemPrompt = `You are an AI assistant with access to a secure microVM environment. You can create VMs, execute commands inside them, and manage their lifecycle.

## Available Tools

- **vm.create** — Create a new microVM and assign it to the current conversation. Requires an environment type (e.g., "alpine", "ubuntu").
- **vm.start** — Resume a previously used VM from this conversation's history.
- **vm.list** — List all VMs currently assigned to this conversation.
- **vm.execute_command** — Execute a shell command inside a running VM. Requires vm_id and command. This is your main workhorse for doing actual work.
- **vm.stop** — Stop a VM and release it from the conversation.

## Workflow

Follow this workflow for any task that requires running code or commands:

1. **Get a VM**: Call vm.list first. If no VMs are available, call vm.create ONCE with a suitable environment (e.g., "ubuntu" for general tasks, "alpine" for lightweight tasks). Do NOT create multiple VMs.
2. **Execute commands**: Use vm.execute_command with the vm_id to do all your work — write files, install packages, run programs, etc. You can chain multiple vm.execute_command calls.
3. **Report results**: Once all commands are done, respond with your final answer summarizing what was done.

## Rules

- For general questions that don't need code execution, respond directly without calling any tool.
- Create ONLY ONE VM per conversation. Reuse it for all commands.
- Always prefer calling a tool over describing what you would do.
- If a tool call fails, analyze the error and retry with different arguments.
- Never fabricate tool results. Only report what the tool actually returned.
- Be efficient with steps. Minimize unnecessary vm.list or vm.create calls.`

// buildSystemInstruction resolves the system instruction to use.
// Priority: history-extracted system messages > agent system prompt > hardcoded default.
func buildSystemInstruction(systemInstr, agentPrompt string) *genai.Content {
	text := systemInstr
	if text == "" {
		text = agentPrompt
	}
	if text == "" {
		text = defaultSystemPrompt
	}
	return &genai.Content{Parts: []*genai.Part{{Text: text}}}
}

// Planner implements ports.ToolPlanner using the Gemini API.
type Planner struct {
	client *genai.Client
	config Config
	tools  ports.ToolRegistry
}

// NewPlanner creates a Gemini-backed tool planner.
// Falls back to defaults from cfg for zero-value fields.
func NewPlanner(ctx context.Context, cfg Config, tools ports.ToolRegistry) (*Planner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  cfg.APIKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("gemini: create client: %w", err)
	}

	return &Planner{
		client: client,
		config: cfg,
		tools:  tools,
	}, nil
}


// PlanTools sends the conversation to Gemini and extracts any function calls
// as planned tool steps. Returns an empty plan when the model responds with
// text only (pure chat mode).
func (p *Planner) PlanTools(ctx context.Context, req ports.PlanRequest) (ports.ToolPlan, error) {
	plan, _, err := p.PlanToolsWithMetadata(ctx, req)
	return plan, err
}

// PlanToolsWithMetadata sends the conversation to Gemini and extracts function
// calls plus optional reasoning/token usage metadata.
func (p *Planner) PlanToolsWithMetadata(ctx context.Context, req ports.PlanRequest) (ports.ToolPlan, ports.PlanMetadata, error) {
	logger := pkglog.FromContext(ctx)

	contents, systemInstr := convertHistory(req.History)

	// Append current user message.
	contents = append(contents, &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: req.Message}},
	})

	genConfig := &genai.GenerateContentConfig{
		Temperature:       genai.Ptr(float32(0)),
		MaxOutputTokens:   p.config.MaxOutputTokens,
		SystemInstruction: buildSystemInstruction(systemInstr, p.config.SystemPrompt),
	}

	// Tool declarations (may be nil if registry is empty).
	if p.tools != nil {
		genConfig.Tools = buildTools(p.tools)
	}

	logger.DebugContext(ctx, "PlanTools: sending to Gemini", "model", p.config.Model, "content_turns", len(contents), "has_tools", genConfig.Tools != nil)

	resp, err := p.client.Models.GenerateContent(ctx, p.config.Model, contents, genConfig)
	if err != nil {
		return ports.ToolPlan{}, ports.PlanMetadata{}, fmt.Errorf("gemini: plan tools: %w", err)
	}

	metadata := ports.PlanMetadata{
		TokenUsage: tokenUsageFromResponse(resp),
	}

	// Log structured LLM output: thinking, text answer, and function calls.
	var steps []ports.ToolPlanStep
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			// Capture and log model thinking.
			if part.Thought && part.Text != "" {
				metadata.Thinking = part.Text
				logger.DebugContext(ctx, "LLM thinking", "model", p.config.Model, "thought", part.Text)
			}
			// Capture and log text answer (non-thought text parts).
			if !part.Thought && part.Text != "" && part.FunctionCall == nil {
				metadata.Answer = part.Text
				logger.DebugContext(ctx, "LLM answer", "model", p.config.Model, "text", part.Text)
			}
			// Extract function calls.
			if part.FunctionCall == nil {
				continue
			}
			args := part.FunctionCall.Args
			if args == nil {
				args = map[string]any{}
			}
			logger.DebugContext(ctx, "LLM function call", "model", p.config.Model, "tool", part.FunctionCall.Name, "arguments", args)
			steps = append(steps, ports.ToolPlanStep{
				Name:             part.FunctionCall.Name,
				Arguments:        args,
				ThoughtSignature: part.ThoughtSignature,
			})
		}
	}

	if len(steps) == 0 {
		// Only call resp.Text() for text-only responses to avoid the SDK warning
		// about non-text parts.
		metadata.RawText = resp.Text()
		return ports.ToolPlan{}, metadata, nil
	}

	logger.DebugContext(ctx, "LLM token usage", "model", p.config.Model, "prompt_tokens", metadata.TokenUsage.PromptTokens, "completion_tokens", metadata.TokenUsage.CompletionTokens, "total_tokens", metadata.TokenUsage.TotalTokens, "thoughts_tokens", metadata.TokenUsage.ThoughtsTokens)
	return ports.ToolPlan{Steps: steps}, metadata, nil
}

// FinalResponse sends conversation history plus tool results to Gemini and
// returns the synthesized text response.
func (p *Planner) FinalResponse(ctx context.Context, req ports.FinalResponseRequest) (string, error) {
	text, _, err := p.FinalResponseWithMetadata(ctx, req)
	return text, err
}

// FinalResponseWithMetadata sends conversation history plus tool results to
// Gemini and returns synthesized text with optional token usage metadata.
func (p *Planner) FinalResponseWithMetadata(ctx context.Context, req ports.FinalResponseRequest) (string, ports.FinalResponseMetadata, error) {
	logger := pkglog.FromContext(ctx)

	contents, systemInstr := convertHistory(req.History)

	// Append user message.
	contents = append(contents, &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: req.Message}},
	})

	// Append model turn with planned function calls.
	if len(req.Plan.Steps) > 0 {
		fcParts := make([]*genai.Part, len(req.Plan.Steps))
		for i, step := range req.Plan.Steps {
			fcParts[i] = &genai.Part{
				FunctionCall: &genai.FunctionCall{
					Name: step.Name,
					Args: step.Arguments,
				},
				ThoughtSignature: step.ThoughtSignature,
			}
		}
		contents = append(contents, &genai.Content{
			Role:  genai.RoleModel,
			Parts: fcParts,
		})
	}

	// Append tool results as a user turn.
	if len(req.ToolResults) > 0 {
		frParts := make([]*genai.Part, len(req.ToolResults))
		for i, result := range req.ToolResults {
			frParts[i] = &genai.Part{
				FunctionResponse: &genai.FunctionResponse{
					Name:     result.ToolName,
					Response: toolResultToResponse(result),
				},
			}
		}
		contents = append(contents, &genai.Content{
			Role:  genai.RoleUser,
			Parts: frParts,
		})
	}

	genConfig := &genai.GenerateContentConfig{
		Temperature:       genai.Ptr(responseTemperature),
		MaxOutputTokens:   p.config.MaxOutputTokens,
		SystemInstruction: buildSystemInstruction(systemInstr, p.config.SystemPrompt),
	}

	// Log tool results being sent back to LLM for synthesis.
	for _, result := range req.ToolResults {
		logger.DebugContext(ctx, "LLM tool result", "model", p.config.Model, "tool", result.ToolName, "ok", result.OK, "output", toolResultToResponse(result))
	}

	resp, err := p.client.Models.GenerateContent(ctx, p.config.Model, contents, genConfig)
	if err != nil {
		return "", ports.FinalResponseMetadata{}, fmt.Errorf("gemini: final response: %w", err)
	}

	metadata := ports.FinalResponseMetadata{
		Reasoning:  resp.Text(),
		TokenUsage: tokenUsageFromResponse(resp),
	}

	text := resp.Text()
	if text == "" {
		logger.DebugContext(ctx, "FinalResponse: empty response from model", "model", p.config.Model)
		return "[no response]", metadata, nil
	}

	// Log structured final response output.
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.Thought && part.Text != "" {
				metadata.Thinking = part.Text
				logger.DebugContext(ctx, "LLM thinking (final)", "model", p.config.Model, "thought", part.Text)
			}
		}
	}
	logger.DebugContext(ctx, "LLM final answer", "model", p.config.Model, "answer", text, "prompt_tokens", metadata.TokenUsage.PromptTokens, "completion_tokens", metadata.TokenUsage.CompletionTokens, "total_tokens", metadata.TokenUsage.TotalTokens, "thoughts_tokens", metadata.TokenUsage.ThoughtsTokens)
	return text, metadata, nil
}

func tokenUsageFromResponse(resp *genai.GenerateContentResponse) orchdomain.TokenUsage {
	if resp == nil || resp.UsageMetadata == nil {
		return orchdomain.TokenUsage{}
	}

	u := resp.UsageMetadata
	return orchdomain.TokenUsage{
		PromptTokens:        int(u.PromptTokenCount),
		CompletionTokens:    int(u.CandidatesTokenCount),
		TotalTokens:         int(u.TotalTokenCount),
		CachedTokens:        int(u.CachedContentTokenCount),
		ThoughtsTokens:      int(u.ThoughtsTokenCount),
		ToolUsePromptTokens: int(u.ToolUsePromptTokenCount),
	}
}
