/**
 * Parse diff3-style conflict markers (requires merge.conflictStyle=diff3 so the
 * common ancestor is present — that base region is what lets us do a precise
 * 3-way union instead of a blind pick).
 *
 *   <<<<<<< ours
 *   ...ours...
 *   ||||||| base
 *   ...base...
 *   =======
 *   ...theirs...
 *   >>>>>>> theirs
 */
export interface ConflictHunk {
  oursLabel: string;
  theirsLabel: string;
  ours: string[];
  base: string[];
  theirs: string[];
  /** A few lines immediately before/after the hunk, for resolver context. */
  contextBefore: string[];
  contextAfter: string[];
}

const OURS = /^<<<<<<< (.*)$/;
const BASE = /^\|\|\|\|\|\|\| (.*)$/;
const SEP = /^=======\s*$/;
const THEIRS = /^>>>>>>> (.*)$/;

const CONTEXT = 3;

export function hasConflictMarkers(content: string): boolean {
  return OURS.test(content.split("\n").find((l) => OURS.test(l)) ?? "");
}

export function parseConflicts(content: string): ConflictHunk[] {
  const lines = content.split("\n");
  const hunks: ConflictHunk[] = [];
  let i = 0;

  while (i < lines.length) {
    const m = lines[i].match(OURS);
    if (!m) {
      i++;
      continue;
    }
    const oursLabel = m[1];
    const ours: string[] = [];
    const base: string[] = [];
    const theirs: string[] = [];
    let theirsLabel = "theirs";
    const startIdx = i;
    i++;

    while (i < lines.length && !BASE.test(lines[i]) && !SEP.test(lines[i])) {
      ours.push(lines[i++]);
    }
    if (i < lines.length && BASE.test(lines[i])) {
      i++;
      while (i < lines.length && !SEP.test(lines[i])) base.push(lines[i++]);
    }
    if (i < lines.length && SEP.test(lines[i])) {
      i++;
      while (i < lines.length && !THEIRS.test(lines[i])) theirs.push(lines[i++]);
    }
    if (i < lines.length) {
      theirsLabel = lines[i].match(THEIRS)?.[1] ?? "theirs";
      i++;
    }

    const contextBefore = lines.slice(Math.max(0, startIdx - CONTEXT), startIdx);
    const contextAfter = lines.slice(i, Math.min(lines.length, i + CONTEXT));

    hunks.push({ oursLabel, theirsLabel, ours, base, theirs, contextBefore, contextAfter });
  }

  return hunks;
}

/**
 * Replace each conflict region in `content` with the resolved text, in order.
 * `resolutions[k]` corresponds to the k-th hunk returned by parseConflicts.
 */
export function applyResolutions(content: string, resolutions: string[]): string {
  const lines = content.split("\n");
  const out: string[] = [];
  let i = 0;
  let r = 0;

  while (i < lines.length) {
    if (OURS.test(lines[i])) {
      // Skip the whole conflict block.
      while (i < lines.length && !THEIRS.test(lines[i])) i++;
      if (i < lines.length) i++; // consume the >>>>>>> line
      const resolved = resolutions[r++] ?? "";
      if (resolved.length) out.push(...resolved.split("\n"));
    } else {
      out.push(lines[i++]);
    }
  }

  return out.join("\n");
}
