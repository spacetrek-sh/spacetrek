package orchestratorsvc

import (
	"context"
	"fmt"
	"strings"
	"time"

	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	"github.com/kumori-sh/spacetrk/src/core/domain/session"
	"github.com/kumori-sh/spacetrk/src/core/domain/tool"
	"github.com/kumori-sh/spacetrk/src/core/ports"
)

// ProcessInput is one runtime turn passed to the orchestrator.
type ProcessInput struct {
	SessionID string
	AgentID   string
	UserID    string
	Message   string
	VMID      string
	History   []session.Message
	EmitEvent func(event orchdomain.RuntimeEvent)
}

// ProcessResult is the orchestrator output for one user turn.
type ProcessResult struct {
	ToolResults      []tool.Result
	AssistantMessage string
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
func NewConfig(allowedTools []string, toolTimeout time.Duration) Config {
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
		MaxReactSteps: 6,
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
		cfg.MaxReactSteps = 6
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

	current, err := s.states.Load(ctx, input.SessionID)
	if err != nil {
		logger.WarnContext(ctx, "failed to load conversation state", "session_id", input.SessionID, "error", err)
		return ProcessResult{}, err
	}
	current.SessionID = input.SessionID
	current.UpdatedAt = time.Now().UTC()
	if err := s.states.Save(ctx, current); err != nil {
		logger.ErrorContext(ctx, "failed to save conversation state", "session_id", input.SessionID, "error", err)
		return ProcessResult{}, err
	}

	return s.processReactLoop(ctx, input)
}

func (s *Service) processReactLoop(ctx context.Context, input ProcessInput) (ProcessResult, error) {
	logger := pkglog.FromContext(ctx)

	currentMessage := input.Message
	executedSteps := make([]ports.ToolPlanStep, 0)
	toolResults := make([]tool.Result, 0)

	for step := 1; step <= s.config.MaxReactSteps; step++ {
		plan, err := s.planner.PlanTools(ctx, ports.PlanRequest{
			SessionID: input.SessionID,
			AgentID:   input.AgentID,
			UserID:    input.UserID,
			Message:   currentMessage,
			VMID:      input.VMID,
			History:   input.History,
		})
		if err != nil {
			logger.ErrorContext(ctx, "planner failed", "session_id", input.SessionID, "step", step, "error", err)
			return ProcessResult{}, err
		}

		if len(plan.Steps) == 0 {
			break
		}

		// ReAct loop executes one action at a time so the next plan can use the latest observation.
		next := plan.Steps[0]
		executedSteps = append(executedSteps, next)

		emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
			Type:      orchdomain.EventToolStart,
			SessionID: input.SessionID,
			ToolName:  next.Name,
			Data:      fmt.Sprintf("react_step=%d", step),
			At:        time.Now().UTC(),
		})

		result := s.executeStep(ctx, next)
		toolResults = append(toolResults, result)

		logger.DebugContext(ctx, "react step executed", "session_id", input.SessionID, "step", step, "tool", next.Name, "ok", result.OK)

		observation := ""
		if payload, ok := result.Payload.(map[string]any); ok {
			if out, ok := payload["output"].(string); ok {
				observation = out
			}
		}
		if observation == "" && result.Error != "" {
			observation = "error: " + result.Error
		}

		if observation != "" {
			emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
				Type:      orchdomain.EventToolStdout,
				SessionID: input.SessionID,
				ToolName:  next.Name,
				Data:      observation,
				At:        time.Now().UTC(),
			})
		}

		emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
			Type:      orchdomain.EventToolEnd,
			SessionID: input.SessionID,
			ToolName:  next.Name,
			Success:   result.OK,
			Error:     result.Error,
			At:        time.Now().UTC(),
		})

		currentMessage = buildReactObservationMessage(input.Message, step, next, result)
	}

	assistant, err := s.planner.FinalResponse(ctx, ports.FinalResponseRequest{
		Message: input.Message,
		Plan: ports.ToolPlan{
			Steps: executedSteps,
		},
		ToolResults: toolResults,
		History:     input.History,
	})
	if err != nil {
		logger.ErrorContext(ctx, "final response generation failed", "session_id", input.SessionID, "steps", len(executedSteps), "error", err)
		return ProcessResult{}, err
	}

	emitRuntimeEvent(input.EmitEvent, orchdomain.RuntimeEvent{
		Type:      orchdomain.EventLLMToken,
		SessionID: input.SessionID,
		Data:      assistant,
		At:        time.Now().UTC(),
	})

	state := orchdomain.State{
		SessionID: input.SessionID,
		StepCount: len(executedSteps),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.states.Save(ctx, state); err != nil {
		logger.ErrorContext(ctx, "failed to save conversation state after react loop", "session_id", input.SessionID, "error", err)
		return ProcessResult{}, err
	}

	logger.InfoContext(ctx, "react loop completed", "session_id", input.SessionID, "steps", len(executedSteps))

	return ProcessResult{
		ToolResults:      toolResults,
		AssistantMessage: assistant,
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

func buildReactObservationMessage(original string, step int, executed ports.ToolPlanStep, result tool.Result) string {
	observation := ""
	if payload, ok := result.Payload.(map[string]any); ok {
		if out, ok := payload["output"].(string); ok {
			observation = out
		}
	}
	if observation == "" && result.Error != "" {
		observation = result.Error
	}
	observation = strings.TrimSpace(observation)
	if observation == "" {
		observation = "(no observation)"
	}

	return fmt.Sprintf("%s\n\n[react_step=%d]\nexecuted_tool=%s\nobservation=%s", original, step, executed.Name, observation)
}
