package gemini

import (
	"context"
	"fmt"

	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/ports"
	"google.golang.org/genai"
)

const defaultSystemPrompt = `You are an AI assistant operating inside a secure microVM environment.

## Behavior

You have access to tools that let you execute actions inside the VM (run commands, inspect files, install packages, etc).

Follow these rules:
- For general questions, conversation, or explanations — respond directly. Do NOT call any tool.
- When the user asks you to perform an action (run a command, check something, modify something), use the appropriate tool.
- Always prefer calling a tool over describing what command you would run.
- If a tool call fails, analyze the error and decide whether to retry with different arguments or explain the failure to the user.
- Never fabricate tool results. Only report what the tool actually returned.

## ReAct Loop

You are running inside a ReAct (Reason-Act-Observe) loop:
1. **Reason** about the user's request and decide the next action.
2. **Act** by calling exactly one tool per step.
3. **Observe** the tool result, then reason again.

Continue the loop until you have enough information to give a complete answer, or until no further action is needed.

When the user's request is fully addressed, respond with your final answer without calling any tool.`

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
	logger := pkglog.FromContext(ctx)

	contents, systemInstr := convertHistory(req.History)

	// Append current user message.
	contents = append(contents, &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: req.Message}},
	})

	genConfig := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr(float32(0)),
		MaxOutputTokens:  p.config.MaxOutputTokens,
		SystemInstruction: buildSystemInstruction(systemInstr, p.config.SystemPrompt),
	}

	// Tool declarations (may be nil if registry is empty).
	if p.tools != nil {
		genConfig.Tools = buildTools(p.tools)
	}

	resp, err := p.client.Models.GenerateContent(ctx, p.config.Model, contents, genConfig)
	if err != nil {
		logger.Error("PlanTools: Gemini API call failed", "model", p.config.Model, "error", err)
		return ports.ToolPlan{}, fmt.Errorf("gemini: plan tools: %w", err)
	}

	// Extract function calls from the response.
	fcs := resp.FunctionCalls()
	if len(fcs) == 0 {
		return ports.ToolPlan{}, nil
	}

	steps := make([]ports.ToolPlanStep, len(fcs))
	for i, fc := range fcs {
		args := fc.Args
		if args == nil {
			args = map[string]any{}
		}
		steps[i] = ports.ToolPlanStep{
			Name:      fc.Name,
			Arguments: args,
		}
	}

	logger.Debug("PlanTools: function calls found", "count", len(fcs), "tools", stepNames(steps))
	return ports.ToolPlan{Steps: steps}, nil
}

// FinalResponse sends conversation history plus tool results to Gemini and
// returns the synthesized text response.
func (p *Planner) FinalResponse(ctx context.Context, req ports.FinalResponseRequest) (string, error) {
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
		Temperature:      genai.Ptr(responseTemperature),
		MaxOutputTokens:  p.config.MaxOutputTokens,
		SystemInstruction: buildSystemInstruction(systemInstr, p.config.SystemPrompt),
	}

	// No tool declarations — we want a text response.
	resp, err := p.client.Models.GenerateContent(ctx, p.config.Model, contents, genConfig)
	if err != nil {
		logger.Error("FinalResponse: Gemini API call failed", "model", p.config.Model, "error", err)
		return "", fmt.Errorf("gemini: final response: %w", err)
	}

	text := resp.Text()
	if text == "" {
		return "[no response]", nil
	}
	return text, nil
}

// stepNames extracts tool names from plan steps for logging.
func stepNames(steps []ports.ToolPlanStep) []string {
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Name
	}
	return names
}
