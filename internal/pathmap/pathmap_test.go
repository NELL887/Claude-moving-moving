package pathmap

import "testing"

func TestEncodeProjectPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/Users/nell/code/my-app", "-Users-nell-code-my-app"},
		{`C:\Users\nell\my-app`, "C--Users-nell-my-app"},
		{`D:\Work\foo.bar\sub_dir/`, "D--Work-foo-bar-sub-dir"},
	}
	for _, tc := range cases {
		if got := EncodeProjectPath(tc.in); got != tc.want {
			t.Errorf("EncodeProjectPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeGitRemote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"git@github.com:Alice/MyRepo.git", "github.com/alice/myrepo"},
		{"https://github.com/Alice/MyRepo.git", "github.com/alice/myrepo"},
		{"https://github.com/Alice/MyRepo/", "github.com/alice/myrepo"},
	}
	for _, tc := range cases {
		if got := NormalizeGitRemote(tc.in); got != tc.want {
			t.Errorf("NormalizeGitRemote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBaseNameCrossOS(t *testing.T) {
	if got := BaseNameCrossOS(`C:\Users\alice\my-app\`); got != "my-app" {
		t.Fatalf("got %q", got)
	}
	if got := BaseNameCrossOS(`/Users/alice/my-app`); got != "my-app" {
		t.Fatalf("got %q", got)
	}
}
