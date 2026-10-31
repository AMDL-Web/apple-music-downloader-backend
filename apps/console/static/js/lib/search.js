// Pure helpers for catalog search on the New download page (GET search and
// GET artists/{id}/albums): the term the text box holds, a searcher that
// keeps one search in flight and pages each group, an artist's album pages,
// what a hit row shows and where the arrow keys move; which job a hit is in
// the library or the queue through is in lib/search-jobs.js. No DOM; the
// caller passes the network functions.

import { releaseYear, trackLength, count } from './format.js';

/** The longest term the backend searches (GET search `term`). */
export const SEARCH_MAX_TERM = 200;
/** Typing pauses this long before a search starts. */
export const SEARCH_DEBOUNCE_MS = 300;
/** Hits per group on the first page: six fill one, two, three or six columns. */
export const SEARCH_FIRST = 6;
/** Hits per page of a group shown alone, read as its end comes near. */
export const SEARCH_MORE = 25;
/** Albums per page of an artist. */
export const ARTIST_PAGE = 25;
/** Answers kept per tab, oldest dropped first. */
export const SEARCH_CACHE_MAX = 30;

/** The result groups in the order they are shown, with their words. */
export const SEARCH_GROUPS = [
  { id: 'albums', label: 'Albums', one: 'album', many: 'albums' },
  { id: 'songs', label: 'Songs', one: 'song', many: 'songs' },
  { id: 'playlists', label: 'Playlists', one: 'playlist', many: 'playlists' },
  { id: 'artists', label: 'Artists', one: 'artist', many: 'artists' },
];

/** The group #/new?group= shows alone: one of SEARCH_GROUPS' ids, else '' (every group). */
export function searchGroup(value) {
  return SEARCH_GROUPS.some((g) => g.id === value) ? value : '';
}

/**
 * What the text box searches for: {term, tooLong}. The term is its words
 * joined by single spaces; it is '' when the box holds a link (`entries`, a
 * scanLinks result, which then previews links as before) or only space, and
 * when it runs past SEARCH_MAX_TERM characters (tooLong).
 */
export function searchTerm(text, entries) {
  if (Array.isArray(entries) && entries.length) return { term: '', tooLong: false };
  const term = String(text || '')
    .split(/\s+/)
    .filter(Boolean)
    .join(' ');
  if ([...term].length > SEARCH_MAX_TERM) return { term: '', tooLong: true };
  return { term, tooLong: false };
}

/** `group` with `page`'s items after its own (each id once) and `page`'s next_offset. */
export function appendPage(group, page) {
  const items = (group && Array.isArray(group.items) ? group.items : []).slice();
  const seen = new Set(items.map((h) => h && h.id));
  for (const hit of (page && page.items) || []) {
    if (!hit || seen.has(hit.id)) continue;
    seen.add(hit.id);
    items.push(hit);
  }
  const next = page && typeof page.next_offset === 'number' ? page.next_offset : undefined;
  return next ? { items, next_offset: next } : { items };
}

const ABORTED = (err) => Boolean(err && (err.name === 'AbortError' || err.code === 'aborted'));

/** Calls `fn` unless `signal` was aborted first (a run typed past before it started). */
const unlessAborted = (signal, fn) => () => (signal.aborted ? Promise.reject(Object.assign(new Error('aborted'), { name: 'AbortError' })) : fn());

function remember(cache, key, value) {
  cache.delete(key);
  cache.set(key, value);
  while (cache.size > SEARCH_CACHE_MAX) cache.delete(cache.keys().next().value);
}

/**
 * A searcher. `search(query, signal)` returns a promise of a SearchResponse
 * for query {term, types?, offset?, limit}; `onChange()` runs whenever the
 * state changes.
 *
 * - run(term): searches term; an answer cached for it comes back at once.
 *   A run aborts the search still in flight and any "more" of the old term.
 *   '' clears the state.
 * - more(group): reads the group's next page (SEARCH_MORE hits) and appends it.
 * - retry(): searches the current term again (after an error).
 * - state(): {term, status: 'idle'|'loading'|'ready'|'error', data, error,
 *   stale, more}. While a new term loads, `data` keeps the last answer and
 *   `stale` is true, so the list keeps its height until the new one lands.
 *   `more` maps a group to 'loading' or 'error' while a page of it is read
 *   or failed.
 * - dispose(): aborts everything.
 *
 * Answers are cached per term in `cache` (shared between mounts), pages
 * read with more() included; an error is never cached.
 */
export function createSearcher({ search, onChange = () => {}, cache = new Map() }) {
  let st = { term: '', status: 'idle', data: null, error: null, stale: false, more: {} };
  let ctrl = null;
  const moreCtrls = new Map();
  let disposed = false;
  const set = (next) => {
    st = next;
    if (!disposed) onChange();
  };
  const abortMore = () => {
    for (const c of moreCtrls.values()) c.abort();
    moreCtrls.clear();
  };

  function start(term) {
    if (ctrl) ctrl.abort();
    const mine = new AbortController();
    ctrl = mine;
    set({ term, status: 'loading', data: st.data, error: null, stale: Boolean(st.data), more: {} });
    Promise.resolve()
      .then(unlessAborted(mine.signal, () => search({ term, limit: SEARCH_FIRST }, mine.signal)))
      .then(
        (data) => {
          if (ctrl !== mine || mine.signal.aborted) return;
          ctrl = null;
          remember(cache, term, data);
          set({ term, status: 'ready', data, error: null, stale: false, more: {} });
        },
        (error) => {
          if (ctrl !== mine || mine.signal.aborted || ABORTED(error)) return;
          ctrl = null;
          set({ term, status: 'error', data: null, error, stale: false, more: {} });
        },
      );
  }

  return {
    run(term) {
      const t = typeof term === 'string' ? term : '';
      if (t === st.term && st.status !== 'idle' && st.status !== 'error') return;
      abortMore();
      if (!t) {
        if (ctrl) ctrl.abort();
        ctrl = null;
        set({ term: '', status: 'idle', data: null, error: null, stale: false, more: {} });
        return;
      }
      if (cache.has(t)) {
        if (ctrl) ctrl.abort();
        ctrl = null;
        const data = cache.get(t);
        remember(cache, t, data);
        set({ term: t, status: 'ready', data, error: null, stale: false, more: {} });
        return;
      }
      start(t);
    },
    retry() {
      if (st.term && st.status === 'error') start(st.term);
    },
    more(group) {
      const data = st.status === 'ready' ? st.data : null;
      const g = data && data[group];
      if (!g || !g.next_offset || moreCtrls.has(group)) return;
      const term = st.term;
      const mine = new AbortController();
      moreCtrls.set(group, mine);
      set({ ...st, more: { ...st.more, [group]: 'loading' } });
      Promise.resolve()
        .then(unlessAborted(mine.signal, () => search({ term, types: group, offset: g.next_offset, limit: SEARCH_MORE }, mine.signal)))
        .then(
          (page) => {
            if (moreCtrls.get(group) !== mine || mine.signal.aborted) return;
            moreCtrls.delete(group);
            const next = { ...st.data, [group]: appendPage(st.data[group], page && page[group]) };
            remember(cache, term, next);
            const more = { ...st.more };
            delete more[group];
            set({ ...st, data: next, more });
          },
          (error) => {
            if (moreCtrls.get(group) !== mine || mine.signal.aborted || ABORTED(error)) return;
            moreCtrls.delete(group);
            set({ ...st, more: { ...st.more, [group]: 'error' } });
          },
        );
    },
    state: () => st,
    dispose() {
      disposed = true;
      if (ctrl) ctrl.abort();
      abortMore();
    },
  };
}

/**
 * An artist's albums, page by page. `load(id, query, signal)` returns a
 * promise of an ArtistAlbumsResponse for query {offset?, limit}.
 *
 * - open(id): shows that artist (a cached one at once); '' closes it.
 * - more(): reads the next page.
 * - retry(): reads the first page again after an error.
 * - state(): {id, status: 'idle'|'loading'|'ready'|'error', artist, albums,
 *   error, more: '' | 'loading' | 'error'}.
 */
export function createArtistPages({ load, onChange = () => {}, cache = new Map() }) {
  let st = { id: '', status: 'idle', artist: null, albums: null, error: null, more: '' };
  let ctrl = null;
  let disposed = false;
  const set = (next) => {
    st = next;
    if (!disposed) onChange();
  };
  const abort = () => {
    if (ctrl) ctrl.abort();
    ctrl = null;
  };

  function first(id) {
    abort();
    const mine = new AbortController();
    ctrl = mine;
    set({ id, status: 'loading', artist: null, albums: null, error: null, more: '' });
    Promise.resolve()
      .then(unlessAborted(mine.signal, () => load(id, { limit: ARTIST_PAGE }, mine.signal)))
      .then(
        (resp) => {
          if (ctrl !== mine || mine.signal.aborted) return;
          ctrl = null;
          const entry = { artist: resp.artist, albums: appendPage(null, resp.albums) };
          remember(cache, id, entry);
          set({ id, status: 'ready', ...entry, error: null, more: '' });
        },
        (error) => {
          if (ctrl !== mine || mine.signal.aborted || ABORTED(error)) return;
          ctrl = null;
          set({ id, status: 'error', artist: null, albums: null, error, more: '' });
        },
      );
  }

  return {
    open(id) {
      const want = typeof id === 'string' ? id : '';
      if (want === st.id && st.status !== 'idle') return;
      abort();
      if (!want) {
        set({ id: '', status: 'idle', artist: null, albums: null, error: null, more: '' });
        return;
      }
      if (cache.has(want)) {
        const entry = cache.get(want);
        set({ id: want, status: 'ready', ...entry, error: null, more: '' });
        return;
      }
      first(want);
    },
    retry() {
      if (st.id && st.status === 'error') first(st.id);
    },
    more() {
      if (st.status !== 'ready' || !st.albums || !st.albums.next_offset || ctrl) return;
      const id = st.id;
      const mine = new AbortController();
      ctrl = mine;
      set({ ...st, more: 'loading' });
      Promise.resolve()
        .then(unlessAborted(mine.signal, () => load(id, { offset: st.albums.next_offset, limit: ARTIST_PAGE }, mine.signal)))
        .then(
          (resp) => {
            if (ctrl !== mine || mine.signal.aborted) return;
            ctrl = null;
            const entry = { artist: st.artist, albums: appendPage(st.albums, resp.albums) };
            remember(cache, id, entry);
            set({ ...st, ...entry, more: '' });
          },
          (error) => {
            if (ctrl !== mine || mine.signal.aborted || ABORTED(error)) return;
            ctrl = null;
            set({ ...st, more: 'error' });
          },
        );
    },
    state: () => st,
    dispose() {
      disposed = true;
      abort();
    },
  };
}

/**
 * Quality hints from a hit's audio_traits, in this order: "Hi-Res Lossless"
 * (or "Lossless" without it), then "Dolby Atmos". Lossy-only hits have none.
 */
export function qualityHints(traits) {
  const list = Array.isArray(traits) ? traits : [];
  const out = [];
  if (list.includes('hi-res-lossless')) out.push('Hi-Res Lossless');
  else if (list.includes('lossless')) out.push('Lossless');
  if (list.includes('atmos')) out.push('Dolby Atmos');
  return out;
}

const plural = (n, one, many) => `${count(n)} ${n === 1 ? one : many}`;

/**
 * What a hit row shows: {title, facts, explicit, quality}. facts is the meta
 * line in order as {key, text, auto} (auto: Apple's text, shown dir=auto):
 * a song's artist, album and length; an album's artist, year and track
 * count; a playlist's curator; an artist's genre.
 */
export function hitView(hit) {
  if (!hit || typeof hit !== 'object') return null;
  const facts = [];
  const add = (key, text, auto = false) => {
    if (text) facts.push({ key, text, auto });
  };
  if (hit.type === 'song') {
    add('artist', hit.artist_name, true);
    // A single's album is named after the song: saying it twice says nothing.
    if (hit.album_name !== hit.name) add('album', hit.album_name, true);
    add('length', trackLength(hit.duration_ms));
  } else if (hit.type === 'album') {
    add('artist', hit.artist_name, true);
    add('year', releaseYear(hit));
    add('tracks', typeof hit.track_count === 'number' && hit.track_count > 0 ? plural(hit.track_count, 'track', 'tracks') : '');
  } else if (hit.type === 'playlist') {
    // Its curator; the catalog states no track count for a playlist.
    add('curator', hit.artist_name, true);
  } else if (hit.type === 'artist') {
    add('genre', hit.genre || 'Artist', Boolean(hit.genre));
  }
  return {
    title: hit.name || '',
    facts,
    explicit: hit.content_rating === 'explicit',
    quality: hit.type === 'artist' ? [] : qualityHints(hit.audio_traits),
  };
}

const itemsOf = (data, id) => (data && data[id] && Array.isArray(data[id].items) ? data[id].items : []);

/** Every hit a search answer holds, pages read for a group shown alone included. */
export function allHits(data) {
  return SEARCH_GROUPS.flatMap((g) => itemsOf(data, g.id));
}

/**
 * The hits of a search answer in the order they are shown (and the arrow
 * keys move): every group's first SEARCH_FIRST, or all of `group`'s when it
 * is shown alone.
 */
export function shownHits(data, group = '') {
  if (group) return itemsOf(data, group);
  return SEARCH_GROUPS.flatMap((g) => itemsOf(data, g.id).slice(0, SEARCH_FIRST));
}

/** Whether a group's title row offers Show more: it has hits past the first SEARCH_FIRST, read or not. */
export function hasMore(group) {
  return Boolean(group && ((group.next_offset || 0) > 0 || (Array.isArray(group.items) && group.items.length > SEARCH_FIRST)));
}

/**
 * The foot under a group shown alone: 'loading' or 'error' while its next
 * page is read or failed, 'wait' while more pages are to come, '' at the end.
 */
export function groupFoot(group, more) {
  if (more === 'loading' || more === 'error') return more;
  return group && group.next_offset > 0 ? 'wait' : '';
}

/**
 * The count line while searching, announced: "Searching…", "6 albums, 6
 * songs and 1 artist for “Yorushika”", "Nothing found for “xyz”", the
 * artist's album count in the artist view, for a group shown alone "31
 * albums so far for “Yorushika”" or "No playlists for “Yorushika”", ''
 * otherwise.
 */
export function searchCountLine(st, artist = null, group = '') {
  if (artist) {
    if (artist.status === 'loading') return 'Loading albums…';
    if (artist.status !== 'ready' || !artist.albums) return '';
    const n = artist.albums.items.length;
    const name = artist.artist && artist.artist.name ? artist.artist.name : 'This artist';
    return `${name}: ${plural(n, 'album', 'albums')}${artist.albums.next_offset ? ' so far' : ''}`;
  }
  if (!st || !st.term) return '';
  if (st.status === 'loading') return 'Searching…';
  if (st.status !== 'ready' || !st.data) return '';
  const alone = SEARCH_GROUPS.find((g) => g.id === group);
  if (alone) {
    const n = itemsOf(st.data, alone.id).length;
    if (!n) return `No ${alone.many} for “${st.term}”`;
    return `${plural(n, alone.one, alone.many)}${st.data[alone.id].next_offset ? ' so far' : ''} for “${st.term}”`;
  }
  const parts = SEARCH_GROUPS.map((g) => {
    const n = Math.min(itemsOf(st.data, g.id).length, SEARCH_FIRST);
    return n ? plural(n, g.one, g.many) : '';
  }).filter(Boolean);
  if (!parts.length) return `Nothing found for “${st.term}”`;
  const list = parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}` : parts[0];
  return `${list} for “${st.term}”`;
}


/**
 * Where an arrow key moves among the search's focus stops, by where they
 * are drawn: `stops` are {top, bottom, left, right, group} boxes in page
 * order, `at` the index of the one with focus. ArrowLeft and ArrowRight
 * step to the previous or next stop of the same group; ArrowDown takes,
 * among the stops that start below this one's bottom, the nearest row, and
 * in it the one nearest across; ArrowUp the same upward. Home and End go to
 * the first and last. Returns an index, -1 for "leave the hits upward" (the
 * box), or `at` when there is nowhere to go.
 */
export function nextStop(stops, at, key) {
  const here = stops[at];
  if (!here) return at;
  if (key === 'Home') return 0;
  if (key === 'End') return stops.length - 1;
  if (key === 'ArrowLeft' || key === 'ArrowRight') {
    const step = key === 'ArrowRight' ? 1 : -1;
    const to = at + step;
    return stops[to] && stops[to].group === here.group ? to : at;
  }
  if (key !== 'ArrowDown' && key !== 'ArrowUp') return at;
  const down = key === 'ArrowDown';
  const mid = (b) => (b.left + b.right) / 2;
  let best = -1;
  let bestDy = Infinity;
  let bestDx = Infinity;
  stops.forEach((b, i) => {
    if (i === at) return;
    const dy = down ? b.top - here.bottom : here.top - b.bottom;
    if (dy < -1) return;
    const dx = Math.abs(mid(b) - mid(here));
    // Rows within a few pixels of each other are one row.
    if (dy < bestDy - 4 || (Math.abs(dy - bestDy) <= 4 && dx < bestDx)) {
      best = i;
      bestDy = Math.min(dy, bestDy);
      bestDx = dx;
    }
  });
  if (best >= 0) return best;
  return down ? at : -1;
}
