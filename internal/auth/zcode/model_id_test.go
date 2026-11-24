package zcode

import "testing"

func TestOfficialModelID(t *testing.T) {
	cases := []struct {
		name, id, display, want string
	}{
		{"known lowercase id", "glm-5.3", "", "GLM-5.3"},
		{"known flash id", "glm-5.3-flash", "ignored friendly name", "GLM-5.3-Flash"},
		{"official spelling that is lowercase in the app", "GLM-5.3-FlashX", "", "glm-5.3-flashx"},
		{"mixed-case input", "glm-4.7-flashx", "", "GLM-4.7-FlashX"},
		{"already official", "GLM-5.2", "", "GLM-5.2"},
		{"new id with case-only display name", "glm-5.4", "GLM-5.4", "GLM-5.4"},
		{"new id with friendly display name stays as is", "glm-6", "GLM Six", "glm-6"},
		{"new id without display name stays as is", "glm-6", "", "glm-6"},
		{"foreign id passes through", "claude-sonnet-4", "", "claude-sonnet-4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OfficialModelID(tc.id, tc.display); got != tc.want {
				t.Errorf("OfficialModelID(%q, %q) = %q, want %q", tc.id, tc.display, got, tc.want)
			}
		})
	}
}
