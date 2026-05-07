package orchestratorsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/chat"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
)

// ProcessInput is one runtime turn passed to the orchestrator.
type ProcessInput struct {
	ChatID           string
	AgentID          string
	UserID           string
	Message          string
	VMID             string
	EnvironmentHint  string
	History          []chat.Message
	EmitEvent        func(event orchdomain.RuntimeEvent)
}

// ProcessResult is the orchestrator output for one user turn.
type ProcessResult struct {
	ToolResults      []tool.Result
	AssistantMessage string
	Trace            *orchdomain.ExecutionTrace
}

// Service coordinates planner decisions, tool execution, and state persistence.
type Service struct {
	planner ports.ToolPlanner
	tools   ports.ToolRegistry
	states  ports.ConversationStateStore
	config  Config
}

// Config contains safety guardrails for tool execution.
type Config struct {
	AllowedTools  map[string]struct{}
	ToolTimeout   time.Duration
	MaxReactSteps int
}

// NewConfig creates Config from simple inputs.
func NewConfig(allowedTools []string, toolTimeout time.Duration, maxReactSteps int) Config {
	allow := make(map[string]struct{}, len(allowedTools))
	for _, name := range allowedTools {
		if name == "" {
			continue
		}
		allow[name] = struct{}{}
	}

	return Config{
		AllowedTools:  allow,
		ToolTimeout:   toolTimeout,
		MaxReactSteps: maxReactSteps,
	}
}

// New creates an orchestrator service with safe defaults.
func New(planner ports.ToolPlanner, tools ports.ToolRegistry, states ports.ConversationStateStore) *Service {
	return NewWithConfig(planner, tools, states, Config{})
}

// NewWithConfig creates an orchestrator service with explicit guardrails.
func NewWithConfig(planner ports.ToolPlanner, tools ports.ToolRegistry, states ports.ConversationStateStore, cfg Config) *Service {
	if planner == nil {
		planner = NewRulePlanner()
	}
	if tools == nil {
		tools = NewInMemoryToolRegistry(nil)
	}
	if states == nil {
		states = NewMemoryStateStore()
	}
	if cfg.ToolTimeout <= 0 {
		cfg.ToolTimeout = 30 * time.Second
	}
	if cfg.MaxReactSteps <= 0 {
		cfg.MaxReactSteps = 30
	}

	return &Service{
		planner: planner,
		tools:   tools,
		states:  states,
		config:  cfg,
	}
}

// Process runs a ReAct loop for one user turn and returns the assistant message.
func (s *Service) Process(ctx context.Context, input ProcessInput) (ProcessResult, error) {
	logger := pkglog.FromContext(ctx)
	logger.DebugContext(ctx, "orchestrator: process started", "chat_id", input.ChatID, "message", input.Message)

	current, err := s.states.Load(ctx, input.ChatID)
	if err != nil {
		logger.WarnContext(ctx, "failed to load conversation state", "chat_id", input.ChatID, "error", err)
		return ProcessResult{}, err
	}
	current.ChatID = input.ChatID
	current.UpdatedAt = time.Now().UTC()
	if err := s.states.Save(ctx, current); err != nil {
		logger.ErrorContext(ctx, "failed to save conversation state", "chat_id", input.ChatID, "error", err)
		return ProcessResult{}, err
	}

	return s.processReactLoop(ctx, input)
}

func (s *Service) processReactLoop(ctx context.Context, input ProcessInput) (ProcessResult, error) {
	ctx = tool.WithChatID(ctx, input.ChatID)
	logger := pkglog.FromContext(ctx)
	logger.DebugContext(ctx, "orchestrator: react loop started", "chat_id", input.ChatID, "max_steps", s.config.MaxReactSteps)

	trace := orchdomain.ExecutionTrace{
		TraceID:       uuid.NewString(),
		ExecutionMode: "react_loop",
		StartedAt:     time.Now().UTC(),
	}

	executedSteps := make([]ports.ToolPlanStep, 0)
	toolResults := make([]tool.Result, 0)
	priorTurns := make([]ports.PriorTurn, 0)
	metaPlanner, hasMetadataPlanner := s.planner.(ports.ToolPlannerWithMetadata)

	for step := 1; step <= s.config.MaxReactSteps; step++ {
		logger.DebugContext(ctx, "orchestrator: calling planner", "chat_id", input.ChatID, "step", step)

		planReq := ports.PlanRequest{
			ChatID:          input.ChatID,
			AgentID:         input.AgentID,
			UserID:          input.UserID,
			Message:         input.Message,
			VMID:            input.VMID,
			EnvironmentHint: input.EnvironmentHint,
			History:         input.History,
			PriorTurns:      priorTurns,
		}

		var (
			plan     ports.ToolPlan
			planMeta ports.PlanMetadata
			err      error
		)
		if hasMetadataPlanner {
			plan, planMeta, err = metaPlanner.PlanToolsWithMetadata(ctx, planReq)
		} else {
			plan, err = s.planner.PlanTools(ctx, planReq)
		}
		if err != nil {
			logger.ErrorContext(ctx, "planner failed", "chat_id", input.ChatID, "step", step, "error", err)
			return ProcessResult{}, err
		}
		trace.TokenUsage.Add(planMeta.TokenUsage)

		if len(plan.Steps) == 0 {
			logger.DebugContext(ctx, "orchestrator: planner returned no tool steps, exiting loop", "chat_id", input.ChatID, "step", step)
			break
		}

		// Emit thinking and answer events for this planning step.
		if planMeta.Thinking != "" {
			emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
				Type:    orchdomain.EventThinking,
				ChatID:  input.ChatID,
				TraceID: trace.TraceID,
				Step:    step,
				Data:    planMeta.Thinking,
				At:      time.Now().UTC(),
			})
		}
		if planMeta.Answer != "" {
			emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
				Type:    orchdomain.EventAnswer,
				ChatID:  input.ChatID,
				TraceID: trace.TraceID,
				Step:    step,
				Data:    planMeta.Answer,
				At:      time.Now().UTC(),
			})
		}

		// ReAct loop executes one action at a time so the next plan can use the latest observation.
		next := plan.Steps[0]
		executedSteps = append(executedSteps, next)

		result := s.executeStep(ctx, next)
		toolResults = append(toolResults, result)
		priorTurns = append(priorTurns, ports.PriorTurn{
			ToolCall:   next,
			ToolResult: result,
		})

		logger.DebugContext(ctx, "react step executed", "chat_id", input.ChatID, "step", step, "tool", next.Name, "ok", result.OK)

		observation := ""
		if payload, ok := result.Payload.(map[string]any); ok {
			if out, ok := payload["output"].(string); ok {
				observation = out
			} else if raw, err := json.Marshal(payload); err == nil {
				observation = string(raw)
			}
		}
		if observation == "" && result.Error != "" {
			observation = "error: " + result.Error
		}

		// Only emit tool_call for vm.execute_command.
		if next.Name == "vm.execute_command" {
			cmd, _ := next.Arguments["command"].(string)
			emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
				Type:    orchdomain.EventToolCall,
				ChatID:  input.ChatID,
				TraceID: trace.TraceID,
				Step:    step,
				Command: cmd,
				Result:  observation,
				Error:   result.Error,
				At:      time.Now().UTC(),
			})
		}

		trace.Steps = append(trace.Steps, orchdomain.TraceStep{
			Step:          step,
			Reasoning:     planMeta.Reasoning,
			ToolName:      next.Name,
			ToolArguments: next.Arguments,
			Observation:   observation,
			ToolSuccess:   result.OK,
			ToolError:     result.Error,
		})
		if planMeta.Reasoning != "" {
			trace.Reasoning = planMeta.Reasoning
		}
	}

	finalReq := ports.FinalResponseRequest{
		Message: input.Message,
		Plan: ports.ToolPlan{
			Steps: executedSteps,
		},
		ToolResults: toolResults,
		History:     input.History,
		EnvironmentHint: input.EnvironmentHint,
	}

	var (
		assistant string
		finalMeta ports.FinalResponseMetadata
		err       error
	)
	if hasMetadataPlanner {
		assistant, finalMeta, err = metaPlanner.FinalResponseWithMetadata(ctx, finalReq)
	} else {
		assistant, err = s.planner.FinalResponse(ctx, finalReq)
	}
	if err != nil {
		logger.ErrorContext(ctx, "final response generation failed", "chat_id", input.ChatID, "steps", len(executedSteps), "error", err)
		return ProcessResult{}, err
	}
	trace.TokenUsage.Add(finalMeta.TokenUsage)
	if finalMeta.Reasoning != "" {
		trace.Reasoning = finalMeta.Reasoning
	}

	// Emit final thinking event.
	if finalMeta.Thinking != "" {
		emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
			Type:    orchdomain.EventThinking,
			ChatID:  input.ChatID,
			TraceID: trace.TraceID,
			Step:    len(trace.Steps) + 1,
			Data:    finalMeta.Thinking,
			At:      time.Now().UTC(),
		})
	}

	trace.FinalAnswer = assistant
	trace.CompletedAt = time.Now().UTC()

	logger.DebugContext(ctx, "orchestrator: final response generated", "chat_id", input.ChatID, "steps", len(executedSteps), "response_len", len(assistant))

	var tokenUsage *orchdomain.TokenUsage
	if !trace.TokenUsage.IsZero() {
		copyUsage := trace.TokenUsage
		tokenUsage = &copyUsage
	}

	emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
		Type:       orchdomain.EventToken,
		ChatID:     input.ChatID,
		TraceID:    trace.TraceID,
		Data:       assistant,
		TokenUsage: tokenUsage,
		At:         time.Now().UTC(),
	})

	state := orchdomain.State{
		ChatID:    input.ChatID,
		StepCount: len(executedSteps),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.states.Save(ctx, state); err != nil {
		logger.ErrorContext(ctx, "failed to save conversation state after react loop", "chat_id", input.ChatID, "error", err)
		return ProcessResult{}, err
	}

	logger.InfoContext(ctx, "react loop completed", "chat_id", input.ChatID, "steps", len(executedSteps))

	return ProcessResult{
		ToolResults:      toolResults,
		AssistantMessage: assistant,
		Trace:            &trace,
	}, nil
}

func (s *Service) executeStep(ctx context.Context, step ports.ToolPlanStep) tool.Result {
	logger := pkglog.FromContext(ctx)

	if len(s.config.AllowedTools) > 0 {
		if _, allowed := s.config.AllowedTools[step.Name]; !allowed {
			logger.WarnContext(ctx, "tool blocked by policy", "tool", step.Name)
			return tool.Result{
				ToolName: step.Name,
				OK:       false,
				Error:    fmt.Sprintf("tool %q is not allowed by policy", step.Name),
			}
		}
	}

	execTool, ok := s.tools.Get(step.Name)
	if !ok {
		logger.WarnContext(ctx, "tool not registered", "tool", step.Name)
		return tool.Result{
			ToolName: step.Name,
			OK:       false,
			Error:    fmt.Sprintf("tool %q is not registered", step.Name),
		}
	}

	call := tool.Call{
		ID:        fmt.Sprintf("%d", time.Now().UTC().UnixNano()),
		Name:      step.Name,
		Arguments: step.Arguments,
	}

	toolCtx, cancel := context.WithTimeout(ctx, s.config.ToolTimeout)
	defer cancel()

	result, err := execTool.Execute(toolCtx, call)
	if result.ToolCallID == "" {
		result.ToolCallID = call.ID
	}
	if result.ToolName == "" {
		result.ToolName = step.Name
	}
	if err != nil {
		result.OK = false
		if result.Error == "" {
			result.Error = err.Error()
		}
		logger.ErrorContext(ctx, "tool execution failed", "tool", step.Name, "error", err)
	}
	return result
}

func emitRuntimeEvent(emitFn func(event orchdomain.RuntimeEvent), event orchdomain.RuntimeEvent) {
	if emitFn == nil {
		return
	}
	emitFn(event)
}
