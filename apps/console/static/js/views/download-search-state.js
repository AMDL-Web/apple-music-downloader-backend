// New download: the state behind catalog search (views/download-search.js
// draws it). useSearch runs the searcher and an open artist's pages
// (lib/search.js) and keeps the library and queue notes live;
// useHitActions downloads a hit and opens or closes an artist as a history
// entry; toResults takes the text box's keys to the results.

import { useState, useEffect, useRef } from '../ui/preact.js';
import { searchCatalog, getArtistAlbums } from '../api.js';
import { buildHash } from '../lib/route.js';
import { createSearcher, createArtistPages, shownHits } from '../lib/search.js';
import { hitKey, hitPrior, trackJobs, sentJob, hitPreview } from '../lib/search-jobs.js';

// Answers outlive the page for the life of the tab, like link previews.
const SEARCHES = new Map();
const ARTISTS = new Map();

/**
 * The page's search: `term` (lib/search.js searchTerm) is searched once
 * it has been stable for `debounced`; `artistId` (#/new?artist=) opens that
 * artist while there is a term. Returns {search, artist, pending, more,
 * retry, artistMore, artistRetry, runNow, priorOf, recordSent, hits}.
 * `subscribeFeed` keeps the library and queue notes live.
 */
export function useSearch({ term, debounced, artistId, epoch, subscribeFeed }) {
  const [, setTick] = useState(0);
  const rerender = () => setTick((n) => n + 1);
  const searcher = useRef(null);
  const artists = useRef(null);
  if (!searcher.current) {
    searcher.current = createSearcher({ search: (q, signal) => searchCatalog(q, { signal }), cache: SEARCHES, onChange: rerender });
    artists.current = createArtistPages({ load: (id, q, signal) => getArtistAlbums(id, q, { signal }), cache: ARTISTS, onChange: rerender });
  }
  useEffect(
    () => () => {
      searcher.current.dispose();
      artists.current.dispose();
    },
    [],
  );
  useEffect(() => searcher.current.run(debounced), [debounced]);
  useEffect(() => artists.current.open(term ? artistId || '' : ''), [artistId, Boolean(term)]);
  // After a feed resync a failed search or artist runs again.
  const firstEpoch = useRef(epoch);
  useEffect(() => {
    if (firstEpoch.current === epoch) return;
    searcher.current.retry();
    artists.current.retry();
  }, [epoch]);

  // Jobs this page submitted for hits, and the latest snapshot of every job
  // a shown hit names, kept live by the feed.
  const [sent, setSent] = useState(() => new Map());
  const [jobs, setJobs] = useState(() => new Map());
  const search = searcher.current.state();
  const artist = artists.current.state();
  const hits = [...shownHits(search.data), ...(artist.albums ? artist.albums.items : []), ...(artist.artist ? [artist.artist] : [])];
  const watched = useRef(new Set());
  watched.current = new Set([...sent.values(), ...hits.map((h) => h.prior_job && h.prior_job.id).filter(Boolean)]);
  useEffect(() => subscribeFeed((batch) => setJobs((m) => trackJobs(m, batch, watched.current))), []);

  return {
    search,
    artist,
    // Typed but not searched yet (the debounce): the card shows it is coming.
    pending: Boolean(term) && term !== search.term,
    more: (group) => searcher.current.more(group),
    retry: () => searcher.current.retry(),
    artistMore: () => artists.current.more(),
    artistRetry: () => artists.current.retry(),
    runNow: () => searcher.current.run(term),
    priorOf: (hit) => hitPrior(hit, sent, jobs),
    recordSent: (hit, job) => {
      setSent((m) => new Map(m).set(hitKey(hit), job.id));
      setJobs((m) => new Map(m).set(job.id, job));
    },
  };
}

/**
 * The page's side of the hits. download(hit) sends a song's or album's url
 * alone through the page's `send` and `report` (its result joins the
 * results), and asks first through `setConfirm` for an artist, whose url
 * queues every album as an artist link does. openArtist(hit) opens the
 * artist over the results as a history entry (#/new?artist=), so Back, the
 * browser's or the card's (closeArtist), returns to them. Returns {busy
 * (urls being sent), download, openArtist, closeArtist, openedHit (the hit
 * the open artist came from, for its head until the answer), focusArtist
 * (whether this page opened it)}.
 */
export function useHitActions({ app, route, term, artistId, search, send, report, overrides, setConfirm, clearNotes }) {
  const [busy, setBusy] = useState(() => new Set());
  const sendHit = async (hit) => {
    if (busy.has(hit.url)) return;
    setConfirm(null);
    clearNotes();
    setBusy((b) => new Set(b).add(hit.url));
    const { pairs, fail } = await send([hit.url], overrides);
    setBusy((b) => new Set([...b].filter((u) => u !== hit.url)));
    const job = pairs.length ? sentJob(pairs[0].result) : null;
    if (job) search.recordSent(hit, job);
    report(pairs, fail, () => hitPreview(hit), true);
  };
  const download = (hit) =>
    hit.type === 'artist' ? setConfirm({ names: [hit.name], label: 'Download all', run: () => sendHit(hit) }) : sendHit(hit);

  const opened = useRef(null);
  const pushed = useRef(false);
  const openArtist = (hit) => {
    opened.current = hit;
    pushed.current = true;
    app.navigate(buildHash('new', { ...route.params, q: term, artist: hit.id }, route.jobId));
  };
  const closeArtist = () => {
    if (pushed.current) window.history.back();
    else app.navigate(buildHash('new', { ...route.params, artist: '' }, route.jobId), { replace: true });
  };
  useEffect(() => {
    if (!artistId) pushed.current = false;
  }, [artistId]);

  return {
    busy,
    download,
    openArtist,
    closeArtist,
    openedHit: opened.current && opened.current.id === artistId ? opened.current : null,
    // An artist this page opened (not one a reload or a link opened) takes focus.
    focusArtist: pushed.current,
  };
}

/**
 * The text box's keys in a search: Return, and ArrowDown on its last line,
 * go to the first hit; Return searches at once while the answer for `term`
 * is not in yet.
 */
export function toResults(e, term, s) {
  if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
  const first = () => document.querySelector('.new-search [data-hit-primary]:not(:disabled):not([aria-disabled="true"])');
  const el = e.currentTarget;
  if (e.key === 'Enter') {
    e.preventDefault();
    const stop = s.search.term === term && s.search.status === 'ready' ? first() : null;
    if (stop) stop.focus();
    else s.runNow();
  } else if (e.key === 'ArrowDown' && !el.value.slice(el.selectionEnd).includes('\n')) {
    const stop = first();
    if (stop) {
      e.preventDefault();
      stop.focus();
    }
  }
}
