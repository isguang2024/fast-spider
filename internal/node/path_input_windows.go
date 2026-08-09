//go:build windows

package node

func normalizeMachinePathInput(path string) string {
	if len(path) == 2 && path[1] == ':' {
		letter := path[0]
		if (letter >= 'A' && letter <= 'Z') || (letter >= 'a' && letter <= 'z') {
			return path + `\`
		}
	}
	return path
}
