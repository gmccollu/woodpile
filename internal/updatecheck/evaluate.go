package updatecheck

import "strings"

// Latest is the newest release this build would offer.
type Latest struct {
	Version    string `json:"version"`
	Prerelease bool   `json:"prerelease"`
	URL        string `json:"url"` // releasesURL + "/tag/" + the tag, or releasesURL
}

// evaluate picks the newest release this build would offer from f: stable for a
// release build; for a prerelease build, the newer of stable and prerelease.
// Nothing from the file reaches the UI but the parsed version: the link is
// releasesURL + "/tag/" + the tag when html_url says exactly that, and
// releasesURL when it says something else on the releases page. An html_url
// outside the releases page means the file isn't ours: an error, no banner
// (decided, build, 2026-11-13).
func evaluate(f *releasesFile, current Version, releasesURL string) (*Latest, *CheckError) {
	type cand struct {
		v   Version
		tag string
		url string
	}
	var cands []cand
	consider := func(e *releaseEntry, wantPre bool, which string) *CheckError {
		if e == nil {
			return nil
		}
		v, ok := ParseVersion(e.TagName)
		if !ok || !strings.HasPrefix(e.TagName, "v") && e.TagName != v.String() {
			return &CheckError{Kind: KindBadJSON, Msg: "the " + which + " release's tag isn't a version"}
		}
		if v.IsPrerelease() != wantPre {
			return &CheckError{Kind: KindBadJSON, Msg: "the " + which + " release has the wrong kind of version"}
		}
		link, ok := releaseLink(releasesURL, e.TagName, e.HTMLURL)
		if !ok {
			return &CheckError{Kind: KindBadJSON, Msg: "the " + which + " release links outside the releases page"}
		}
		cands = append(cands, cand{v: v, tag: e.TagName, url: link})
		return nil
	}
	if err := consider(f.Stable, false, "stable"); err != nil {
		return nil, err
	}
	if current.IsPrerelease() {
		if err := consider(f.Prerelease, true, "prerelease"); err != nil {
			return nil, err
		}
	}
	if len(cands) == 0 {
		return nil, &CheckError{Kind: KindNoRelease, Msg: "the releases file lists no release"}
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.v.Compare(best.v) > 0 {
			best = c
		}
	}
	return &Latest{Version: best.v.String(), Prerelease: best.v.IsPrerelease(), URL: best.url}, nil
}

// releaseLink is the banner's link for a tag (see evaluate). It reports false
// for an html_url outside releasesURL.
func releaseLink(releasesURL, tag, htmlURL string) (string, bool) {
	want := releasesURL + "/tag/" + tag
	switch {
	case htmlURL == want:
		return want, true
	case htmlURL == "" || htmlURL == releasesURL || strings.HasPrefix(htmlURL, releasesURL+"/"):
		return releasesURL, true
	}
	return "", false
}
