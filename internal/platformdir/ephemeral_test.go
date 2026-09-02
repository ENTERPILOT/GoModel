package platformdir

import (
	"strings"
	"testing"
)

// A trimmed mountinfo from a container with a volume at /app/data and a
// bind-mounted directory whose name has a space.
const containerMountinfo = `
1077 1076 0:245 / / rw,relatime master:301 - overlay overlay rw,lowerdir=/x,upperdir=/y,workdir=/z
1078 1077 0:249 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
1079 1077 0:250 / /dev rw,nosuid - tmpfs tmpfs rw,size=65536k,mode=755
1083 1077 0:32 /var/lib/docker/volumes/gomodel_data/_data /app/data rw,relatime master:1 - ext4 /dev/vda1 rw
1084 1077 0:32 /home/me/my\040configs /app/config rw,relatime - ext4 /dev/vda1 rw
1085 1077 0:251 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw
`

func TestMountFilesystem(t *testing.T) {
	cases := []struct {
		dir, want string
	}{
		{"/app/data", "ext4"},        // the volume itself
		{"/app/data/sub", "ext4"},    // below the volume
		{"/app/database", "overlay"}, // shares a prefix but is not inside the mount
		{"/app", "overlay"},          // the image layer
		{"/app/config/prod", "ext4"}, // bind mount with an escaped space in its source
		{"/tmp/scratch", "tmpfs"},    // scratch space
		{"/", "overlay"},             // the root
	}
	for _, tc := range cases {
		got, ok := mountFilesystem(strings.NewReader(containerMountinfo), tc.dir)
		if !ok || got != tc.want {
			t.Errorf("mountFilesystem(%q) = %q, %v; want %q", tc.dir, got, ok, tc.want)
		}
	}
}

func TestMountFilesystemIgnoresMalformedLines(t *testing.T) {
	got, ok := mountFilesystem(strings.NewReader("garbage\n1 2 3 4\n"), "/app")
	if ok || got != "" {
		t.Errorf("mountFilesystem on garbage = %q, %v; want no match", got, ok)
	}
}

func TestUnescapeMountPath(t *testing.T) {
	cases := map[string]string{
		`/plain`:         "/plain",
		`/with\040space`: "/with space",
		`/tab\011here`:   "/tab\there",
		`/trailing\04`:   `/trailing\04`,
		`/not\xyz`:       `/not\xyz`,
	}
	for in, want := range cases {
		if got := unescapeMountPath(in); got != want {
			t.Errorf("unescapeMountPath(%q) = %q, want %q", in, got, want)
		}
	}
}
