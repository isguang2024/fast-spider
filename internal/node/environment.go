package node

import (
	"os"
	"runtime"
	"sort"
	"strings"
)

var shellEnvironmentAllowlist = map[string]struct{}{
	"APPDATA": {}, "BUN_INSTALL": {}, "CARGO_HOME": {}, "COMSPEC": {},
	"DOTNET_ROOT": {}, "GOROOT": {}, "GOPATH": {}, "GRADLE_HOME": {},
	"HOME": {}, "HOMEDRIVE": {}, "HOMEPATH": {}, "JAVA_HOME": {},
	"LANG": {}, "LC_ALL": {}, "LC_CTYPE": {}, "LOCALAPPDATA": {},
	"LOGNAME": {}, "MAVEN_HOME": {}, "NUMBER_OF_PROCESSORS": {},
	"NVM_HOME": {}, "NVM_SYMLINK": {}, "PATH": {}, "PATHEXT": {},
	"PNPM_HOME": {}, "PROCESSOR_ARCHITECTURE": {}, "PROGRAMDATA": {},
	"PROGRAMFILES": {}, "PROGRAMFILES(X86)": {}, "PROGRAMW6432": {},
	"RUSTUP_HOME": {}, "SHELL": {}, "SYSTEMROOT": {}, "TEMP": {},
	"TMP": {}, "TMPDIR": {}, "TZ": {}, "USER": {}, "USERDOMAIN": {},
	"USERNAME": {}, "USERPROFILE": {}, "WINDIR": {},
}

func safeShellEnvironment() []string {
	values := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		lookup := key
		if runtime.GOOS == "windows" {
			lookup = strings.ToUpper(key)
		}
		if _, allowed := shellEnvironmentAllowlist[lookup]; !allowed {
			continue
		}
		values[lookup] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}
