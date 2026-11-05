package outlook

// outlookExe is classic Outlook's executable: its process name
// (system_windows.go) and the name of its App Paths registration.
const outlookExe = "OUTLOOK.EXE"

// appPathsSubKey is where a Windows application registers the absolute
// path of its executable (the default value), per machine (HKLM) and per
// user (HKCU). Start launches the path classic Outlook registered there
// (host_windows.go), never a bare "OUTLOOK.EXE": the shell would search
// its own path — the working and install directories included — where a
// planted copy could run instead.
const appPathsSubKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\` + outlookExe

// appPathRoot selects one of the two hives an App Paths registration lives
// in.
type appPathRoot int

const (
	appPathHKLM appPathRoot = iota // HKEY_LOCAL_MACHINE: all users
	appPathHKCU                    // HKEY_CURRENT_USER: this user
)

// appPathLookup reads the default (unnamed) value of the App Paths
// registration subkey under root, reporting whether it resolves to a
// string. It is a seam: winHost passes the registry reader
// (registryAppPath), tests a fake, on any platform.
type appPathLookup func(root appPathRoot, subkey string) (string, bool)

// resolveOutlookExe returns classic Outlook's absolute executable path
// from its App Paths registration: the machine's first, then the user's.
// A missing key, a non-string value or an empty one does not resolve;
// when nothing resolves the result is ErrOutlookExeNotFound, never the
// bare executable name the shell would search for.
func resolveOutlookExe(lookup appPathLookup) (string, error) {
	for _, root := range [...]appPathRoot{appPathHKLM, appPathHKCU} {
		if exe, ok := lookup(root, appPathsSubKey); ok && exe != "" {
			return exe, nil
		}
	}
	return "", ErrOutlookExeNotFound
}

// launchOutlook starts classic Outlook through shell, with the absolute
// path its App Paths registration resolved to, verbatim. Only
// Connector.StartOutlook (the user's explicit action) calls it: background
// work never starts Outlook, and the shell is never handed a bare name a
// planted copy could shadow.
func launchOutlook(lookup appPathLookup, shell func(exe string) error) error {
	exe, err := resolveOutlookExe(lookup)
	if err != nil {
		return err
	}
	return shell(exe)
}
