package platformdir

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ephemeralFilesystems are the filesystem types a container's own writable
// layer and its scratch mounts show up as. Anything written there is gone
// when the container is recreated.
var ephemeralFilesystems = map[string]bool{
	"overlay": true,
	"tmpfs":   true,
}

// Ephemeral reports whether dir lives on a filesystem that does not outlive
// the running container: the image's overlay layer, or a tmpfs. A directory
// on a mounted volume, a bind mount, or any ordinary disk reports false, and
// so does every platform without Linux mount tables.
//
// It exists so a deployment that keeps its database or its identity in such a
// directory can be told at startup, before the data is lost, rather than
// after.
func Ephemeral(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	fstype, ok := mountFilesystem(f, abs)
	return ok && ephemeralFilesystems[fstype]
}

// mountFilesystem returns the filesystem type of the mount that holds dir:
// the mount whose mount point is the longest prefix of dir. Lines have the
// form documented in proc(5):
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
//
// The mount point is the fifth field and the type the first field after the
// "-" separator; the optional fields in between vary in number.
func mountFilesystem(mountinfo io.Reader, dir string) (string, bool) {
	best, bestType := "", ""
	scanner := bufio.NewScanner(mountinfo)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		sep := -1
		for i, field := range fields {
			if field == "-" {
				sep = i
				break
			}
		}
		if sep < 5 || sep+1 >= len(fields) {
			continue
		}
		point := unescapeMountPath(fields[4])
		if !withinMount(dir, point) || len(point) < len(best) {
			continue
		}
		best, bestType = point, fields[sep+1]
	}
	return bestType, best != ""
}

// withinMount reports whether dir is point itself or below it.
func withinMount(dir, point string) bool {
	if point == "/" {
		return true
	}
	return dir == point || strings.HasPrefix(dir, point+"/")
}

// unescapeMountPath decodes the octal escapes the kernel uses for spaces and
// a few other characters in mount paths ("\040" for a space).
func unescapeMountPath(path string) string {
	if !strings.Contains(path, `\`) {
		return path
	}
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' && i+3 < len(path) {
			if v, ok := octal(path[i+1 : i+4]); ok {
				out.WriteByte(v)
				i += 3
				continue
			}
		}
		out.WriteByte(path[i])
	}
	return out.String()
}

func octal(s string) (byte, bool) {
	var v int
	for _, c := range s {
		if c < '0' || c > '7' {
			return 0, false
		}
		v = v*8 + int(c-'0')
	}
	return byte(v), true
}
