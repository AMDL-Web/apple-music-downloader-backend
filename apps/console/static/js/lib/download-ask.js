// What a download asks before it goes, by the desktop app's rules
// (apps/desktop/internal/ui/download.go): the quality, when the release has
// both lossless and Dolby Atmos audio and the server's quality_priority lists
// ALAC and a Dolby codec, and what to do with files already on disk, when
// the release is in the library and this browser does not always skip them.
// Pure: no DOM, so node --test covers it.

/** localStorage key of "Always skip files already downloaded" (this browser). */
export const SKIP_KEY = 'amdl.console.skipExisting';

const LOSSLESS = ['lossless', 'hi-res-lossless'];
const DOLBY = ['ec3', 'ac3'];

/** The question's name for a codec. */
export const CHOICE_LABEL = { alac: 'Lossless', ec3: 'Dolby Atmos', ac3: 'Dolby Digital' };

/**
 * The codecs to choose between, in the server's priority order (the first
 * is the default), or [] when there is no choice: the release's audio_traits
 * need lossless and atmos, and quality_priority ALAC and ec3 or ac3.
 */
export function codecChoices(traits, priority) {
  const t = Array.isArray(traits) ? traits : [];
  if (!t.some((x) => LOSSLESS.includes(x)) || !t.includes('atmos')) return [];
  const p = Array.isArray(priority) ? priority : [];
  if (!p.includes('alac') || !p.some((c) => DOLBY.includes(c))) return [];
  return p.filter((c) => c === 'alac' || DOLBY.includes(c));
}

/**
 * What to ask before downloading: {codecs, overwrite}. `follow` is false
 * when the page's quality menu names a quality, which then needs no
 * question; `inLibrary` when the release's last download completed.
 */
export function downloadAsks({ traits, priority, follow = true, inLibrary = false, alwaysSkip = false }) {
  return { codecs: follow ? codecChoices(traits, priority) : [], overwrite: Boolean(inLibrary) && !alwaysSkip };
}

/** Whether a download has a question to ask. */
export const hasAsks = (asks) => Boolean(asks && (asks.codecs.length > 0 || asks.overwrite));

/**
 * The overrides an answer adds: quality_priority led by the chosen codec,
 * then the rest of the server's order; force_overwrite for a release in the
 * library, false unless Replace was chosen, so the server's own setting does
 * not decide it.
 */
export function answerOverrides({ codec = '', replace = false, priority = [], inLibrary = false }) {
  const out = {};
  if (codec) out.quality_priority = [codec, ...priority.filter((c) => c !== codec)];
  if (inLibrary) out.force_overwrite = Boolean(replace);
  return out;
}

/** The stored "always skip" preference: true only for the string 'true'. */
export const readSkip = (value) => value === 'true';
