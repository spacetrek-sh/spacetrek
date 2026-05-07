package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	"google.golang.org/genai"
)

const defaultSystemPrompt = `You are an AI assistant with access to a secure microVM environment. You can create VMs, execute commands inside them, and manage their lifecycle.

## Available Tools

- **vm.create** — Create a new microVM and assign it to the current conversation. Requires an environment type.
  - **"uv"** — Python with uv package manager. Use for data science, scripting, ML, math, file processing, or any Python task.
  - **"bun"** — Bun JS/TS runtime. Use for JavaScript/TypeScript tasks, web dev, or npm ecosystem work.
  - **"ubuntu"** — Generic Ubuntu shell. Use only when no language-specific environment fits (pure shell/awk/sed tasks).
  Pick the most specific environment that matches the task. Prefer "uv" over "ubuntu" for Python work.
- **vm.start** — Resume a previously used VM from this conversation's history.
- **vm.list** — List all VMs currently assigned to this conversation.
- **vm.execute_command** — Execute a shell command inside a running VM. Requires vm_id and command. This is your main workhorse for doing actual work.
- **vm.stop** — Stop a VM and release it from the conversation.

## Environment Constraints

- VMs run in an isolated sandbox with NO internet access. Package managers (apt, apk, pip, npm) will NOT work.
- Only tools and binaries pre-installed in the base image are available. Use "ls /bin && ls /usr/bin" to discover what is available.
{{ENVIRONMENT_HINT}}
- The default working directory is /workspace. Prefer reading, writing, and executing files under /workspace unless the user requests a different path.
- /workspace is persistent across conversation resumes.
- Work within these constraints. If a language or tool is not available, adapt and use what IS available (e.g., use shell/awk instead of Python).

## Workflow

You receive a conversation history that may include prior tool calls and their results from earlier steps in this turn. Use that context — do NOT re-query state you already have.

1. **Check prior turns**: If a previous step already created a VM, reuse that vm_id. Do NOT call vm.list or vm.create again.
2. **Create a VM only once**: If no VM exists yet, call vm.create ONCE with a suitable environment. Do NOT create multiple VMs.
3. **Execute commands**: Use vm.execute_command with the existing vm_id to do all your work — write files, run programs, etc.
4. **Report results**: Once all commands are done, respond with your final answer summarizing what was done.

## Rules

- For general questions that don't need code execution, respond directly without calling any tool.
- NEVER call vm.list after creating a VM or executing a command — the VM ID is already in the conversation.
- NEVER recreate or overwrite files that were already written in a previous step.
- NEVER try to install packages (apt-get, apk, pip, npm) — VMs have no internet access.
- Create ONLY ONE VM per conversation. Reuse it for all commands.
- Always prefer calling a tool over describing what you would do.
- If a tool call fails, analyze the error and try an alternative approach with the available tools. Do NOT repeat the same failing command.
- Never fabricate tool results. Only report what the tool actually returned.
- Be efficient with steps. Every step counts toward a maximum limit.
- ALWAYS respond with a text summary of what you did, even if some steps failed.`

// buildSystemInstruction resolves the system instruction to use.
// Priority: history-extracted system messages > agent system prompt > hardcoded default.
func buildSystemInstruction(systemInstr, agentPrompt, envHint string) *genai.Content {
	text := systemInstr
	if text == "" {
		text = agentPrompt
	}
	if text == "" {
		text = defaultSystemPrompt
	}
	if envHint != "" {
		text = strings.Replace(text, "{{ENVIRONMENT_HINT}}", "- Active environment: "+envHint, 1)
	} else {
		text = strings.Replace(text, "{{ENVIRONMENT_HINT}}\n", "", 1)
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

	// Build user message, injecting VM context if a VM is already available.
	userText := req.Message
	if req.VMID != "" {
		userText = fmt.Sprintf(
			"[System: A VM (id: %s) is already running and assigned to this conversation. "+
				"Use vm.execute_command with this vm_id for any commands. "+
				"Do NOT call vm.create or vm.start.]\n\n%s",
			req.VMID, req.Message,
		)
	}

	// Append current user message.
	contents = append(contents, &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: userText}},
	})

	// Append prior react-loop turns as function call/result pairs so Gemini
	// has multi-turn context within this user turn.
	for _, turn := range req.PriorTurns {
		contents = append(contents, &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					Name: turn.ToolCall.Name,
					Args: turn.ToolCall.Arguments,
				},
				ThoughtSignature: turn.ToolCall.ThoughtSignature,
			}},
		})
		contents = append(contents, &genai.Content{
			Role: genai.RoleUser,
			Parts: []*genai.Part{{
				FunctionResponse: &genai.FunctionResponse{
					Name:     turn.ToolCall.Name,
					Response: toolResultToResponse(turn.ToolResult),
				},
			}},
		})
	}

	genConfig := &genai.GenerateContentConfig{
		Temperature:       genai.Ptr(float32(0)),
		MaxOutputTokens:   p.config.MaxOutputTokens,
		SystemInstruction: buildSystemInstruction(systemInstr, p.config.SystemPrompt, req.EnvironmentHint),
	}

	// Tool declarations (may be nil if registry is empty).
	if p.tools != nil {
		genConfig.Tools = buildTools(p.tools)
	}

	logger.DebugContext(ctx, "PlanTools: sending to Gemini", "model", p.config.Model, "content_turns", len(contents), "prior_turns", len(req.PriorTurns), "has_tools", genConfig.Tools != nil)

	if sysInstr := genConfig.SystemInstruction; sysInstr != nil && len(sysInstr.Parts) > 0 {
		logger.DebugContext(ctx, "LLM input: system instruction", "model", p.config.Model, "text", sysInstr.Parts[0].Text)
	}
	for i, c := range contents {
		role := string(c.Role)
		for j, part := range c.Parts {
			switch {
			case part.Text != "" && part.FunctionCall == nil:
				logger.DebugContext(ctx, "LLM input: content", "model", p.config.Model, "turn", i, "part", j, "role", role, "text", part.Text)
			case part.FunctionCall != nil:
				args, _ := json.Marshal(part.FunctionCall.Args)
				logger.DebugContext(ctx, "LLM input: content", "model", p.config.Model, "turn", i, "part", j, "role", role, "function_call", part.FunctionCall.Name, "arguments", string(args))
			case part.FunctionResponse != nil:
				respJSON, _ := json.Marshal(part.FunctionResponse.Response)
				logger.DebugContext(ctx, "LLM input: content", "model", p.config.Model, "turn", i, "part", j, "role", role, "function_response", part.FunctionResponse.Name, "response", string(respJSON))
			}
		}
	}

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

		// Append an explicit synthesis request so the model always produces text output.
		contents = append(contents, &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{Text: "I have completed my tool calls. Let me summarize the results."}},
		})
		contents = append(contents, &genai.Content{
			Role:  genai.RoleUser,
			Parts: []*genai.Part{{Text: fmt.Sprintf("Based on the tool results above, provide a clear summary of what was accomplished for the user's request: %q. If any steps failed, explain what went wrong and what was tried.", req.Message)}},
		})
	}

	genConfig := &genai.GenerateContentConfig{
		Temperature:       genai.Ptr(responseTemperature),
		MaxOutputTokens:   p.config.MaxOutputTokens,
		SystemInstruction: buildSystemInstruction(systemInstr, p.config.SystemPrompt, req.EnvironmentHint),
	}

	// Log tool results being sent back to LLM for synthesis.
	for _, result := range req.ToolResults {
		logger.DebugContext(ctx, "LLM tool result", "model", p.config.Model, "tool", result.ToolName, "ok", result.OK, "output", toolResultToResponse(result))
	}

	if sysInstr := genConfig.SystemInstruction; sysInstr != nil && len(sysInstr.Parts) > 0 {
		logger.DebugContext(ctx, "LLM input (final): system instruction", "model", p.config.Model, "text", sysInstr.Parts[0].Text)
	}
	for i, c := range contents {
		role := string(c.Role)
		for j, part := range c.Parts {
			switch {
			case part.Text != "" && part.FunctionCall == nil:
				logger.DebugContext(ctx, "LLM input (final): content", "model", p.config.Model, "turn", i, "part", j, "role", role, "text", part.Text)
			case part.FunctionCall != nil:
				args, _ := json.Marshal(part.FunctionCall.Args)
				logger.DebugContext(ctx, "LLM input (final): content", "model", p.config.Model, "turn", i, "part", j, "role", role, "function_call", part.FunctionCall.Name, "arguments", string(args))
			case part.FunctionResponse != nil:
				respJSON, _ := json.Marshal(part.FunctionResponse.Response)
				logger.DebugContext(ctx, "LLM input (final): content", "model", p.config.Model, "turn", i, "part", j, "role", role, "function_response", part.FunctionResponse.Name, "response", string(respJSON))
			}
		}
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
