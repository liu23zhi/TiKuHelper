package proxysource

import (
	"os"
	"testing"
)

func TestReadProxySources(t *testing.T) {
	t.Chdir(t.TempDir())
	write := func(f, s string) {
		t.Helper()
		if err := os.WriteFile(f, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("explicit.txt", "198.51.100.2")
	for _, tc := range []struct {
		desc, env, path, file, want string
		bad                         bool
	}{
		{"absent default is empty", "", "", "", "", false},
		{"default is used", "", "", "\ufeff# trusted\r\n 192.0.2.44 # edge\n", "192.0.2.44", false},
		{"only comments are empty", "", "", "\ufeff# template\r\n // example\n\n", "", false},
		{"file overrides default", "", "explicit.txt", "192.0.2.44", "198.51.100.2", false},
		{"env wins despite absent named file", "203.0.113.3", "absent.txt", "192.0.2.44", "203.0.113.3", false},
		{"explicit absence is an error", "", "absent.txt", "192.0.2.44", "", true},
		{"unparseable content is not silently empty", "", "", "bad input", "bad input", false},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			if tc.file != "" {
				write(DefaultFile, tc.file)
			}
			got, _, err := Read(tc.env, tc.path)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("value=%q err=%v; want=%q bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}
