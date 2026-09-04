package api

import "testing"

func TestValidateInstanceFields(t *testing.T) {
	goodEnv := map[string]string{"JAVA_XMS": "1G"}
	if err := validateInstanceFields("mc", "/srv/mc", []string{"java", "-jar", "server.jar"}, goodEnv, "stop"); err != nil {
		t.Fatalf("good spec rejected: %v", err)
	}
	cases := []struct {
		name string
		fn   func() error
	}{
		{"empty name", func() error {
			return validateInstanceFields("", "/srv/mc", []string{"java"}, nil, "")
		}},
		{"empty cmd", func() error {
			return validateInstanceFields("mc", "/srv/mc", nil, nil, "")
		}},
		{"nul in dir", func() error {
			return validateInstanceFields("mc", "/srv/mc\x00", []string{"java"}, nil, "")
		}},
		{"bad env key", func() error {
			return validateInstanceFields("mc", "/srv/mc", []string{"java"}, map[string]string{"A=B": "x"}, "")
		}},
		{"env newline", func() error {
			return validateInstanceFields("mc", "/srv/mc", []string{"java"}, map[string]string{"A\nB": "x"}, "")
		}},
	}
	for _, c := range cases {
		if err := c.fn(); err == nil {
			t.Fatalf("%s should be rejected", c.name)
		}
	}
	if validFilePath("sub/file.txt") != true {
		t.Fatal("normal path should be valid")
	}
	if validFilePath("a\x00b") != false {
		t.Fatal("NUL path should be invalid")
	}
}
