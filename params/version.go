package params

const (
<<<<<<< HEAD
	Version = "2.0.2"
=======
	Version = "2.0.1"
>>>>>>> master
)

func VersionWithCommit(gitCommit string) string {
	vsn := Version
	if len(gitCommit) >= 8 {
		vsn += "-" + gitCommit[:8]
	}
	return vsn
}
