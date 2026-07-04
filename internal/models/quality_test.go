package models

import "testing"

// TestQualityRank_AZW guards the fix for plain .azw releases scoring 0 in
// ranking: ParseRelease emits "azw" as a distinct format token from "azw3", but
// QualityRank lacked an "azw" key, so scoreResult's QualityRank[quality]*100
// term collapsed a legitimate Kindle release to base score 0.
func TestQualityRank_AZW(t *testing.T) {
	rank, ok := QualityRank["azw"]
	if !ok {
		t.Fatal("QualityRank is missing an \"azw\" entry; plain .azw releases rank 0")
	}
	if rank <= QualityRank["unknown"] {
		t.Errorf("azw rank %d should outrank unknown (%d)", rank, QualityRank["unknown"])
	}
	// azw is the DRM-wrapped mobi predecessor to azw3; rank it with mobi and
	// below azw3.
	if rank != QualityRank["mobi"] {
		t.Errorf("azw rank %d should equal mobi (%d)", rank, QualityRank["mobi"])
	}
	if rank >= QualityRank["azw3"] {
		t.Errorf("azw rank %d should be below azw3 (%d)", rank, QualityRank["azw3"])
	}
}

func TestQualityFromFilename_AZW(t *testing.T) {
	if got := QualityFromFilename("Some Book.azw"); got != "azw" {
		t.Errorf("QualityFromFilename(.azw) = %q, want \"azw\"", got)
	}
}
