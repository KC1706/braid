package determinism

import (
	"sort"
	"strings"

	"github.com/KC1706/braid/pkg/gitx"
)

// TreeHash returns git's content-addressed tree object SHA for ref (e.g. "HEAD").
// Unlike a commit SHA, a tree hash depends ONLY on the file paths, modes and
// contents — not on author/committer identity or timestamps — so it is the right
// "output hash" for a reproducibility assertion: two runs that produce the same
// merged files yield the same tree hash even though their commit SHAs differ.
func TreeHash(repoDir, ref string) (string, error) {
	return gitx.Ok(repoDir, "rev-parse", ref+"^{tree}")
}

// FingerprintTree hashes the merged SOURCE tree at ref, excluding any path under
// one of excludePrefixes. It reads `git ls-tree -r` (mode + blob SHA + path for
// every file), which is content-addressed, so the fingerprint depends only on
// file contents/paths — never on commit metadata. The exclusion exists so the
// non-deterministic provenance artifact (`.braid/trail.json`, which embeds
// timestamps and durations) does not perturb a reproducibility check of the code.
func FingerprintTree(repoDir, ref string, excludePrefixes ...string) (string, error) {
	out, err := gitx.Ok(repoDir, "ls-tree", "-r", ref)
	if err != nil {
		return "", err
	}
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<mode> <type> <sha>\t<path>"
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		path := line[tab+1:]
		excluded := false
		for _, p := range excludePrefixes {
			if strings.HasPrefix(path, p) {
				excluded = true
				break
			}
		}
		if !excluded {
			entries = append(entries, line)
		}
	}
	sort.Strings(entries)
	return ContentHash(entries...), nil
}
