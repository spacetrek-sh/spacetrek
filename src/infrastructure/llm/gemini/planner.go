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

const defaultSystemPrompt = `You are an AI assistant with access to a secure microVM environment. You can create VMs, execute commands inside them, manage files, and orchestrate multi-VM workflows.

## Environment

Each VM is an isolated Firecracker microVM. Networking facts:

- Outbound internet works. Package managers (apt, pip, npm, bun, uv, apk) function normally — install packages on demand when the base image lacks something.
- Public ingress: a VM with a service port set is exposed at https://<vm-name>.box.spacetrek.xyz — Cloudflare Tunnel terminates TLS and forwards to http://<vm-ip>:<service-port> inside the VM. The hostname uses the VM's name (kebab-case, e.g. admiring-turing), NOT its vm_id UUID. When the user wants a public link to a server running in a VM, give them this URL. A VM with no service port is not exposed publicly.
- VMs in the same conversation CAN reach each other, by IP and by DNS name (<peer-name>.vm.internal). Use this for inter-VM calls in multi-tier apps (e.g. a bun frontend VM calling a uv backend VM); use the *.box.spacetrek.xyz URL only for browser/user-facing links.
- The init process is a vsock agent, not systemd. Long-running services do not auto-start; spawn them with nohup or & when needed.

The default working directory is /workspace. Read, write, and execute files under /workspace unless the user asks otherwise. /workspace persists across conversation resumes.

{{ENVIRONMENT_HINT}}

## Available Tools

- **vm.create** — Provision a new VM and assign it to this conversation. Requires an environment type:
  - **"uv"** — Python with uv package manager. Prefer for data science, scripting, ML, math, file processing, any Python task.
  - **"bun"** — Bun JS/TS runtime. Prefer for JavaScript/TypeScript, web dev, npm ecosystem work.
  - **"ubuntu"** — Generic Ubuntu shell. Use only when no language-specific environment fits.
  Pick the most specific environment that matches the task. Prefer "uv" over "ubuntu" for Python.
- **vm.start** — Resume a previously used VM from this conversation's history. Pass a vm_id to resume a specific VM.
- **vm.list** — List VMs assigned to this conversation.
- **vm.execute_command** — Run a shell command inside a running VM. Requires vm_id and command. Main workhorse for doing actual work.
- **vm.write_file** — Write a file inside a VM. Server-side verified; do not re-read to confirm.
- **vm.edit_file** — Apply targeted edits to an existing file. Server-side verified; do not re-read to confirm.
- **vm.read_file** — Read a file's contents from a VM.
- **vm.stop** — Stop a VM and release it from the conversation.
- **vm.snapshot** — Snapshot a VM's state for later restore.
- **memory.set** — Persist a small value (≤4 KB) under a chat-scoped key. **Survives VM snapshot/resume cycles within the same chat** — the next user turn sees what this turn wrote, even after an idle gap. Use it for pointers, intentions, and partial plans: "what files exist", "what was installed", "which VM hosts which service". Keys must match [a-z0-9_:-]{1,64}. Bulk data belongs in /workspace, not here.
- **memory.get** — Read a value previously stored with memory.set. Missing keys return an empty value (no error). Check memory.list first when you do not remember what was stored.
- **memory.delete** — Remove a key. Errors if the key does not exist.
- **memory.list** — Returns every key/value pair currently stored for this chat. Call this at the start of a multi-step turn to reuse prior observations instead of re-querying the VM.

## Current VMs in this conversation

{{VM_INVENTORY}}

## Workflow

You receive a conversation history that may include prior tool calls and their results from earlier steps in this turn. Use that context — do NOT re-query state you already have.

You have a maximum of 10 ReAct steps per turn. Spend them deliberately.

1. **Check memory first**: At the start of a turn, call memory.list to recall pointers stored in earlier turns (file paths, installed packages, VM-to-service mappings, partial plans). This avoids burning a step rediscovering what was already observed. Store new observations with memory.set as you make them.
2. **Check prior turns**: If a previous step already created or started a VM, reuse that vm_id from the tool result. Do not call vm.list, vm.create, or vm.start again.
3. **Select or create a VM**: If the system context lists available VMs, choose one whose environment matches and call vm.start <vm_id>. If none fits, call vm.create with a suitable environment. For multi-tier tasks, create one VM per tier.
4. **Execute**: Use vm.execute_command for shell work; use vm.write_file / vm.edit_file for file changes. Pass the existing vm_id.
5. **Report**: Once done, respond with a concise text summary. Always include a final text answer, even if some steps failed.

## Rules

- For general questions that do not need code execution, respond directly without calling any tool.
- After a successful vm.write_file or vm.edit_file, the change is applied and verified server-side. Do not call vm.read_file to confirm — the tool result already reports bytes/lines/replacements.
- Do not recreate or overwrite files already written in a previous step of this turn.
- If a tool call fails, analyze the error and try an alternative with the available tools. Do not repeat the same failing command.
- Never fabricate tool results. Only report what the tool actually returned.
- Prefer calling a tool over describing what you would do.`

// buildSystemInstruction resolves the system instruction to use.
// Priority: history-extracted system messages > agent system prompt > hardcoded default.
func buildSystemInstruction(systemInstr, agentPrompt, envHint, vmInventory string) *genai.Content {
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
	if vmInventory != "" {
		text = strings.Replace(text, "{{VM_INVENTORY}}", vmInventory, 1)
	} else {
		text = strings.Replace(text, "{{VM_INVENTORY}}", "- None yet. Use vm.create to start a new environment.", 1)
	}
	return &genai.Content{Parts: []*genai.Part{{Text: text}}}
}

// buildVMInventory formats the available VMs as a system-prompt section.
// Returns the body only; the caller places it under the {{VM_INVENTORY}} placeholder.
func buildVMInventory(availableVMs []ports.AvailableVM) string {
	if len(availableVMs) == 0 {
		return ""
	}
	var lines []string
	for _, vm := range availableVMs {
		switch vm.Status {
		case "running", "ready":
			lines = append(lines, fmt.Sprintf(
				"- %s: %s — status=%s, use vm.execute_command with this vm_id",
				vm.VMID, vm.EnvDescription, vm.Status,
			))
		default:
			snap := ""
			if vm.HasSnapshot {
				snap = ", has snapshot"
			}
			lines = append(lines, fmt.Sprintf(
				"- %s: %s — status=%s%s, use vm.start with this vm_id to restore",
				vm.VMID, vm.EnvDescription, vm.Status, snap,
			))
		}
	}
	lines = append(lines, "To start a NEW environment type not listed above, use vm.create.")
	return strings.Join(lines, "\n")
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

	// Append current user message. VM inventory is injected into the system
	// instruction via {{VM_INVENTORY}} so the user turn stays clean.
	contents = append(contents, &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: req.Message}},
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
		SystemInstruction: buildSystemInstruction(systemInstr, p.config.SystemPrompt, req.EnvironmentHint, buildVMInventory(req.AvailableVMs)),
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
		SystemInstruction: buildSystemInstruction(systemInstr, p.config.SystemPrompt, req.EnvironmentHint, ""),
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

const titleSystemPrompt = `Generate a concise 3-6 word title that summarizes the user's message. Respond with the title text only — no quotes, no markdown, no "Title:" prefix, no trailing punctuation.`

const (
	titleTemperature     float32 = 0.2
	titleMaxOutputTokens         = 30
	titleMaxRunes                = 80
)

// GenerateTitle produces a short conversation title from the first user message.
// Falls back to a truncated form of the message when the model returns nothing usable.
func (p *Planner) GenerateTitle(ctx context.Context, message string) (string, error) {
	logger := pkglog.FromContext(ctx)

	contents := []*genai.Content{{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: message}},
	}}

	genConfig := &genai.GenerateContentConfig{
		Temperature:       genai.Ptr(titleTemperature),
		MaxOutputTokens:   int32(titleMaxOutputTokens),
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: titleSystemPrompt}}},
	}

	logger.DebugContext(ctx, "LLM title request", "model", p.config.Model, "message_len", len(message))

	resp, err := p.client.Models.GenerateContent(ctx, p.config.Model, contents, genConfig)
	if err != nil {
		return "", fmt.Errorf("gemini: generate title: %w", err)
	}

	title := sanitizeTitle(resp.Text())
	if title == "" {
		title = fallbackTitle(message)
		logger.DebugContext(ctx, "LLM title empty, using message fallback", "title", title)
	} else {
		logger.DebugContext(ctx, "LLM title generated", "model", p.config.Model, "title", title)
	}
	return title, nil
}

func sanitizeTitle(raw string) string {
	t := strings.TrimSpace(raw)
	t = strings.Trim(t, "\"'`")
	if lower := strings.ToLower(t); strings.HasPrefix(lower, "title:") {
		t = strings.TrimSpace(t[len("Title:"):])
	}
	t = strings.TrimRight(t, ".!?;: ")
	return truncateAtWord(t, titleMaxRunes)
}

func fallbackTitle(message string) string {
	m := strings.TrimSpace(message)
	if m == "" {
		return "New Conversation"
	}
	return truncateAtWord(m, titleMaxRunes)
}

func truncateAtWord(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	cut := maxRunes
	for i := maxRunes - 1; i > 0; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return string(runes[:cut]) + "..."
}
