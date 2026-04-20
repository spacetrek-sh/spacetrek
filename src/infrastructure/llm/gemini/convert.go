package gemini

import (
	"strings"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/chat"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	"google.golang.org/genai"
)

// convertHistory converts domain messages into genai Content entries.
// System messages are extracted separately into a system instruction string.
// Consecutive messages of the same role are merged into one Content with
// multiple Parts, since Gemini requires alternating user/model turns.
func convertHistory(messages []chat.Message) ([]*genai.Content, string) {
	var systemParts []string
	var contents []*genai.Content

	for _, msg := range messages {
		if msg.Role == chat.RoleSystem {
			systemParts = append(systemParts, msg.Content)
			continue
		}

		role := domainRoleToGenai(msg.Role)
		if role == "" {
			continue
		}

		// Merge consecutive same-role messages.
		if len(contents) > 0 && contents[len(contents)-1].Role == role {
			contents[len(contents)-1].Parts = append(
				contents[len(contents)-1].Parts,
				&genai.Part{Text: msg.Content},
			)
			continue
		}

		contents = append(contents, &genai.Content{
			Role: role,
			Parts: []*genai.Part{
				{Text: msg.Content},
			},
		})
	}

	return contents, strings.Join(systemParts, "\n\n")
}

func domainRoleToGenai(r chat.Role) string {
	switch r {
	case chat.RoleUser:
		return genai.RoleUser
	case chat.RoleAssistant:
		return genai.RoleModel
	default:
		return ""
	}
}

// buildTools converts registered tool definitions into genai Tool declarations.
// Returns nil if the registry has no tools.
func buildTools(registry ports.ToolRegistry) []*genai.Tool {
	defs := registry.List()
	if len(defs) == 0 {
		return nil
	}

	declarations := make([]*genai.FunctionDeclaration, 0, len(defs))
	for _, def := range defs {
		declarations = append(declarations, &genai.FunctionDeclaration{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  convertParams(def.Parameters),
		})
	}

	return []*genai.Tool{{FunctionDeclarations: declarations}}
}

// convertParams maps domain tool parameters to a genai Schema.
func convertParams(params map[string]tool.Parameter) *genai.Schema {
	if len(params) == 0 {
		return nil
	}

	properties := make(map[string]*genai.Schema, len(params))
	var required []string
	var ordering []string

	for name, p := range params {
		properties[name] = &genai.Schema{
			Type:        mapParamType(p.Type),
			Description: p.Description,
		}
		ordering = append(ordering, name)
		if p.Required {
			required = append(required, name)
		}
	}

	return &genai.Schema{
		Type:            genai.TypeObject,
		Properties:      properties,
		Required:        required,
		PropertyOrdering: ordering,
	}
}

func mapParamType(t string) genai.Type {
	switch t {
	case "string":
		return genai.TypeString
	case "integer":
		return genai.TypeInteger
	case "number":
		return genai.TypeNumber
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	default:
		return genai.TypeString
	}
}

// toolResultToResponse converts a tool.Result into a response map for genai FunctionResponse.
func toolResultToResponse(result tool.Result) map[string]any {
	if !result.OK {
		return map[string]any{"error": result.Error}
	}
	if m, ok := result.Payload.(map[string]any); ok {
		return m
	}
	return map[string]any{"output": result.Payload}
}
