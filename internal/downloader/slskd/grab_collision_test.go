package slskd

import "testing"

// slskd writes every file of a request into one destination folder, so files
// that share a base name would overwrite or rename each other.
func TestGrabRejectsBasenameCollisions(t *testing.T) {
	cases := map[string][]BatchFile{
		"same name in two folders":  {{Filename: `@@u\A\01.mp3`, Size: 1}, {Filename: `@@u\B\01.mp3`, Size: 1}},
		"differs only by case":      {{Filename: `@@u\A\Track.mp3`, Size: 1}, {Filename: `@@u\A\track.MP3`, Size: 1}},
		"slash and backslash forms": {{Filename: `A/01.mp3`, Size: 1}, {Filename: `B\01.mp3`, Size: 1}},
	}
	for name, files := range cases {
		if _, err := EncodeGrab(Grab{User: "u", Files: files}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := []BatchFile{{Filename: `@@u\A\01.mp3`, Size: 1}, {Filename: `@@u\A\02.mp3`, Size: 1}}
	if _, err := EncodeGrab(Grab{User: "u", Files: ok}); err != nil {
		t.Errorf("distinct names refused: %v", err)
	}
}
