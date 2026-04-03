package ports

import "github.com/kumori-sh/spacetrk/src/core/domain/tool"

// ToolRegistry provides lookup and discovery of registered tools.
type ToolRegistry interface {
	Get(name string) (tool.Tool, bool)
	List() []tool.Definition
}
