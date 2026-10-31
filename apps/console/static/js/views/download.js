// New download (#/new?url=&q=&artist=&group=, #/new/:id for the drawer): paste
// links or search the catalog, choose the quality and submit. One column:
// the compose field and its options, the detected links or the search
// results, the dock (the count, Download; it floats at the bottom while
// the box holds links), then what the last submit did. The text box is the source of the links and the
// term and follows the address both ways; it lasts for the tab. Parts:
// download-hero.js, download-config.js, download-address.js, download-lookup.js,
// download-links.js, download-search*.js, download-preset.js,
// download-quality.js, download-results.js, download-confirm.js; the logic
// is pure in lib/.

import { html, useState, useEffect, useRef, useMemo, useCallback } from '../ui/preact.js';
import { newDownloadUrls, buildHash } from '../lib/route.js';
import { scanLinks, removeLink } from '../lib/links.js';
import { handledUrls, resultRows, submitSummary } from '../lib/submit.js';
import { downloadLabel, servedRegions } from '../lib/submit-check.js';
import { tokenState } from '../lib/user-token.js';
import { optionDefaults, presetOverrides, presetValues, qualityText, linkQuality, dockQuality } from '../lib/overrides.js';
import { isPicked } from '../lib/lookup.js';
import { searchTerm, searchCountLine, shownHits, SEARCH_DEBOUNCE_MS } from '../lib/search.js';
import { artPalette } from '../lib/format.js';
import { useApp } from '../ui/components.js';
import { useDebounced } from '../ui/hooks.js';
import { useStatus } from '../ui/system-status.js';
import { useServerConfig } from './download-config.js';
import { useLookups } from './download-lookup.js';
import { LinksCard } from './download-links.js';
import { QualityControls, CustomSheet } from './download-preset.js';
import { Dock, useBarHeight, useLinkCount } from './download-bar.js';
import { QualitySheet, PROBES } from './download-quality.js';
import { ResultsCard, BrowserShortcut, useResults, sendUrls } from './download-results.js';
import { SearchCard } from './download-search.js';
import { useSearch, useHitActions, toResults } from './download-search-state.js';
import { useAddressSync, useDraft } from './download-address.js';
import { Compose, Options, clearBox } from './download-hero.js';
import { ArtistConfirm } from './download-confirm.js';
import { AskDialog } from './download-ask.js';
import { downloadAsks, hasAsks, answerOverrides } from '../lib/download-ask.js';
import { loadSkipExisting } from '../ui/skip-pref.js';

// The quality choice lasts for the tab too, so the next album can follow
// with the same one: a PRESETS id or 'custom' with its values.
let chosen = { preset: 'follow', custom: null };

export function DownloadPage({ route }) {
  const app = useApp();
  const incoming = newDownloadUrls(route.params);
  // A link without a storefront borrows another link's, else the server's
  // catalog.default_storefront (GET /system), '' until that has loaded.
  const storefront = app.defaultStorefront;
  const [text, setText] = useDraft({ app, incoming, storefront });
  const entries = useMemo(() => scanLinks(text, storefront), [text, storefront]);
  const settled = useDebounced(text, 350);
  // Words and no link: the box searches the catalog.
  const { term, tooLong } = searchTerm(text, entries);
  const searching = Boolean(term) || tooLong;
  const artistId = term ? route.params.artist || '' : '';
  const search = useSearch({ term, debounced: useDebounced(term, SEARCH_DEBOUNCE_MS), artistId, epoch: app.epoch, subscribeFeed: app.subscribeFeed });
  // The submit error under the button (failure) belongs to the text it was
  // sent from and to the connection it failed on: an edit or a reconnect
  // clears it. A submit that drops handled links from the text does not.
  const [failure, setFailure] = useState('');
  const edit = (next) => {
    setFailure('');
    setText(next);
  };
  const epochFailed = useRef(app.epoch);
  useEffect(() => {
    if (epochFailed.current === app.epoch) return;
    epochFailed.current = app.epoch;
    setFailure('');
  }, [app.epoch]);
  const inputRef = useRef(null);

  // The address and the text box follow each other (download-address.js).
  const routeRef = useAddressSync({ app, route, incoming, text, settled, storefront, edit });

  // Focus the text box once the shell has focused the heading (a fine
  // pointer only: on a phone it would open the keyboard unasked).
  useEffect(() => {
    if (!window.matchMedia('(pointer: fine)').matches) return undefined;
    const id = requestAnimationFrame(() => {
      const el = inputRef.current;
      const active = document.activeElement;
      if (!el || routeRef.current.jobId) return;
      if (active && active !== document.body && !active.classList.contains('page-title')) return;
      el.focus({ preventScroll: true });
      el.setSelectionRange(el.value.length, el.value.length);
    });
    return () => cancelAnimationFrame(id);
  }, []);

  // Server defaults.
  const { cfg, setCfgRun } = useServerConfig(app.epoch);

  const download = cfg.data && cfg.data.config ? cfg.data.config.download : null;
  const trackMode = cfg.data && cfg.data.config && cfg.data.config.catalog ? cfg.data.config.catalog.album_track_url_mode || '' : null;
  const defaults = useMemo(() => (download ? optionDefaults(download) : null), [JSON.stringify(download)]);

  // The quality preset.
  const [choice, setChoiceState] = useState(chosen);
  const setChoice = (next) => {
    chosen = next;
    setChoiceState(next);
    setRefusal('');
  };
  const [customOpen, setCustomOpen] = useState(false);
  const overrides = useMemo(() => (defaults ? presetOverrides(defaults, choice.preset, choice.custom) : {}), [defaults, choice]);
  const qualitySelect = () => document.getElementById('new-quality');
  const closeCustom = () => {
    setCustomOpen(false);
    requestAnimationFrame(() => {
      const el = qualitySelect();
      if (el && document.activeElement === document.body) el.focus({ preventScroll: true });
    });
  };

  // The storefronts the wrapper serves, to flag a link before the submit
  // refuses it; null (flag nothing) while unknown or in simulate mode.
  const { wrapper } = useStatus(['wrapper']);
  const regions = servedRegions(wrapper, app.simulate);
  // The media-user-token a download would use: the wrapper's, else Settings'.
  const savedToken = cfg.data && cfg.data.config && cfg.data.config.catalog ? cfg.data.config.catalog.media_user_token || '' : undefined;
  const token = tokenState(savedToken, wrapper);

  const { statusOf, issueOf, sendable: allowed, skipped, queue, priorOf, forgetPrior } = useLookups({
    settled,
    entries,
    token,
    regions,
    trackMode,
    epoch: app.epoch,
    storefront,
    subscribeFeed: app.subscribeFeed,
  });
  const jobHref = (id) => buildHash('new', route.params, id);
  // The checked rows are what the submit sends: a row already in the
  // library or the queue starts unchecked unless the person checks it.
  const [picks, setPicks] = useState(() => new Map());
  const onPick = (url, on) => setPicks((m) => new Map(m).set(url, on));
  const sendable = allowed.filter((u) => isPicked(u, priorOf(u), picks));
  // What the chosen preset resolves to, for the Will download lines.
  const chosenValues = defaults ? presetValues(defaults, choice.preset, choice.custom) : null;
  const previewOf = (url) => {
    const st = statusOf(url);
    return st.state === 'ready' ? st.data : null;
  };

  // Submit and results (kept for the life of the tab).
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState('');
  const [results, setResults] = useResults();
  const [retrying, setRetrying] = useState(() => new Set());
  const [quality, setQuality] = useState(null);
  // Bumped when the quality sheet has read a link's qualities (PROBES).
  const [, setProbed] = useState(0);
  const [confirm, setConfirm] = useState(null);
  const [asking, setAsking] = useState(null);

  /**
   * Runs a download, after the questions it asks (lib/download-ask.js):
   * the quality, while the preset follows Settings, and what to do with
   * files on disk, for a release in the library. run(overrides) sends it
   * with the page's overrides and the answer's.
   */
  const askFirst = ({ title, traits, inLibrary, run }) => {
    const priority = defaults ? defaults.quality_priority : [];
    const asks = downloadAsks({ traits, priority, follow: choice.preset === 'follow', inLibrary, alwaysSkip: loadSkipExisting() });
    const go = (answer) => run({ ...overrides, ...answerOverrides({ ...answer, priority, inLibrary }) });
    if (!hasAsks(asks)) go({});
    else setAsking({ title, asks, go });
  };

  // An artist link queues the whole discography as one job: ask first.
  const artists = sendable.filter((u) => {
    const e = entries.find((x) => x.url === u);
    return e && e.link && e.link.kind === 'artist';
  });
  /**
   * Shows what a submit did: its failure under the bar, its rows in the
   * results (after the rows already there when `keep`, a search hit's
   * download), a toast; returns false when nothing was submitted. `used`
   * are the overrides sent, which a retry sends again.
   */
  const report = (pairs, fail, previewFor, keep = false, used = overrides) => {
    if (fail) {
      if (fail.kind === 'overrides') setRefusal(fail.message);
      else setFailure(fail.message);
    }
    if (!pairs.length) {
      app.toast({ message: fail ? fail.message : 'Nothing was submitted', role: 'danger' });
      return false;
    }
    const rows = resultRows(pairs, previewFor);
    setResults((r) => ({ rows: keep ? [...rows, ...r.rows.filter((x) => !rows.some((n) => n.url === x.url))] : rows, overrides: used }));
    const ids = rows.filter((r) => r.job).map((r) => r.job.id);
    app.toast({
      message: submitSummary(rows),
      role: ids.length ? 'success' : 'warning',
      action: ids.length ? { label: 'View in Queue', hash: '#/queue' } : null,
    });
    if (ids.length && app.watchJobs) app.watchJobs(ids);
    return true;
  };

  const submit = async (confirmed = false) => {
    if (busy) return;
    // Only Cmd/Ctrl+Enter gets here with nothing to send (the button is
    // disabled): say why, as the button does, or what the count line says
    // stops every link.
    if (sendable.length === 0) {
      app.toast({ message: label === 'Download' ? linkCount || "These links can't be downloaded" : label, role: 'warning' });
      return;
    }
    if (artists.length && !confirmed) {
      setConfirm({
        names: artists.map((u) => {
          const s = statusOf(u);
          return (s.state === 'ready' && s.data && s.data.title) || u.replace(/^https?:\/\//i, '');
        }),
        label: `Download ${sendable.length}`,
        run: () => submit(true),
      });
      return;
    }
    setConfirm(null);
    const urls = sendable;
    const one = urls.length === 1 ? previewOf(urls[0]) : null;
    askFirst({
      title: one && one.title ? `Download “${one.title}”` : label,
      traits: [],
      inLibrary: urls.some((u) => (priorOf(u) || {}).state === 'done'),
      run: (used) => sendLinks(urls, used),
    });
  };

  const sendLinks = async (urls, used) => {
    setBusy(true);
    setFailure('');
    setRefusal('');
    const { pairs, fail } = await sendUrls(urls, used);
    setBusy(false);
    if (!report(pairs, fail, previewOf, false, used)) return;
    forgetPrior(pairs.map((p) => p.url));
    // Keep what was not handled: links not sent (flagged, or after a failed
    // chunk), links the server did not take, and anything typed meanwhile.
    // ?url= follows the text.
    setText((t) => [...handledUrls(pairs)].reduce(removeLink, t));
    // The button that had focus is now disabled: the summary takes it, on
    // both submit paths, so the result is where focus lands.
    requestAnimationFrame(() => {
      const el = document.getElementById('new-results-summary');
      if (el) el.focus({ preventScroll: false });
    });
  };

  const askHit = (hit, run) =>
    askFirst({ title: `Download “${hit.name}”`, traits: hit.audio_traits, inLibrary: (search.priorOf(hit) || {}).state === 'done', run });
  const hits = useHitActions({ app, route, term, artistId, search, send: sendUrls, report, ask: askHit, setConfirm, clearNotes: () => (setFailure(''), setRefusal('')) });

  const retry = async (urls) => {
    setRetrying((s) => new Set([...s, ...urls]));
    const { pairs, fail } = await sendUrls(urls, results.overrides);
    setRetrying((s) => new Set([...s].filter((u) => !urls.includes(u))));
    if (fail) app.toast({ message: fail.message, role: 'danger' });
    if (!pairs.length) return;
    const fresh = new Map(resultRows(pairs, previewOf).map((r) => [r.url, r]));
    const ids = [...fresh.values()].filter((r) => r.job).map((r) => r.job.id);
    if (ids.length && app.watchJobs) app.watchJobs(ids);
    setResults((r) => ({ ...r, rows: r.rows.map((row) => fresh.get(row.url) || row) }));
    const done = handledUrls(pairs);
    if (done.size) setText((t) => [...done].reduce(removeLink, t));
    app.toast({ message: submitSummary([...fresh.values()]), role: [...fresh.values()].some((r) => r.job) ? 'success' : 'warning' });
  };

  // Clear removes the card that held focus; the text box takes it (the
  // heading on a touch screen, where focusing the box opens the keyboard).
  const clearResults = () => {
    setResults({ rows: [], overrides: {} });
    requestAnimationFrame(() => {
      if (document.activeElement && document.activeElement !== document.body) return;
      const fine = window.matchMedia('(pointer: fine)').matches;
      const el = fine ? inputRef.current : document.querySelector('.page-title');
      if (el) el.focus({ preventScroll: true });
    });
  };

  const removeEntry = (url) => {
    edit((t) => removeLink(t, url));
    if (inputRef.current) inputRef.current.focus({ preventScroll: true });
  };

  const onKeyDown = (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      submit();
      return;
    }
    // Esc empties the box as one undo step, then leaves it.
    if (e.key === 'Escape' && !e.isComposing) {
      e.preventDefault();
      e.stopPropagation();
      if (e.currentTarget.value) clearBox(e.currentTarget, edit);
      else e.currentTarget.blur();
      return;
    }
    if (term) toResults(e, term, search);
  };

  const linkCount = useLinkCount({ settled, storefront, issueOf, priorOf });
  const kindOf = (url) => {
    const st = statusOf(url);
    if (st.state === 'ready' && st.data && st.data.type) return st.data.type;
    const e = entries.find((x) => x.url === url);
    const link = e && e.link;
    return link ? (link.kind === 'album' && link.trackId && trackMode !== 'album' ? 'song' : link.kind) : 'item';
  };
  const count = searching ? searchCountLine(search.search, search.artist.status !== 'idle' ? search.artist : null, hits.group) : linkCount;
  const label = busy ? 'Submitting…' : downloadLabel(sendable.map(kindOf), entries.length > 0, allowed.length > 0);

  // Under 640 px the dock is fixed above the tab bar; the page keeps room
  // for it below the last row, and toasts rise above it (--new-bar-h on the
  // root, which both read).
  const [barRef, barH] = useBarHeight();
  useEffect(() => {
    const root = document.documentElement.style;
    if (barH) root.setProperty('--new-bar-h', `${barH}px`);
    else root.removeProperty('--new-bar-h');
    return () => root.removeProperty('--new-bar-h');
  }, [barH]);

  // Docked: anything in the box or below it. Empty: the field alone, with
  // the browser shortcut under it.
  const docked = Boolean(text.trim()) || entries.length > 0 || results.rows.length > 0;
  const view = artistId ? 'artist' : searching ? 'search' : entries.length ? 'links' : 'none';
  // The stage takes its glow from the first cover that has a palette.
  const firstPalette = (list) => list.map((x) => artPalette(x)).find(Boolean);
  const palette = searching
    ? firstPalette([...(search.artist.artist ? [search.artist.artist] : []), ...shownHits(search.search.data)])
    : firstPalette(entries.map((e) => previewOf(e.url)).filter(Boolean));
  const tint = palette ? palette.bg : '';
  const style = tint ? { '--stage-tint': tint } : undefined;


  const asks = chosenValues ? qualityText(chosenValues) : '';
  const probedOf = (url) => linkQuality(chosenValues, PROBES.get(url), statusOf(url).data);
  return html`<div class="page page-new" data-mode=${docked ? 'docked' : 'empty'} data-view=${view} data-tinted=${tint ? '' : undefined} style=${style}>
    <div class="new-top">
      <${Compose}
        text=${text}
        inputRef=${inputRef}
        term=${term}
        links=${entries.length}
        busy=${searching && (search.pending || search.search.status === 'loading')}
        storefront=${storefront}
        edit=${edit}
        onKeyDown=${onKeyDown}
      />
      <${Options}
        controls=${html`<${QualityControls}
          defaults=${defaults}
          preset=${choice.preset}
          custom=${choice.custom}
          asks=${asks}
          onPreset=${(preset) => setChoice({ ...choice, preset })}
          onCustom=${() => setCustomOpen(true)}
        />`}
        cfgError=${Boolean(cfg.error && !defaults)}
        onRetryCfg=${() => setCfgRun((n) => n + 1)}
      />
      <p id="new-status" class="sr-only" role="status" aria-live="polite">${count}</p>
    </div>
    ${searching
      ? html`<${SearchCard}
          s=${search}
          term=${term}
          tooLong=${tooLong}
          inputRef=${inputRef}
          openHit=${hits.openedHit}
          focusArtist=${hits.focusArtist}
          row=${{ busy: hits.busy, jobHref, onDownload: hits.download, onOpenArtist: hits.openArtist, priorOf: search.priorOf }}
          onBack=${hits.closeArtist}
          onDownloadAll=${hits.download}
          group=${hits.group}
          onGroup=${hits.openGroup}
          onAllResults=${hits.closeGroup}
        />`
      : null}
    ${entries.length
      ? html`<${LinksCard}
          entries=${entries}
          statusOf=${statusOf}
          issueOf=${issueOf}
          priorOf=${priorOf}
          picked=${(url) => isPicked(url, priorOf(url), picks)}
          skipped=${skipped}
          row=${{
            jobHref,
            onRemove: removeEntry,
            onRetry: (url) => queue.current.retry(url),
            onQuality: (entry, data) => setQuality({ url: entry.url, title: data.title || entry.url, tracks: data.track_count }),
            onPick,
            storefront,
            probed: probedOf,
          }}
        />`
      : null}
    ${entries.length || failure || refusal
      ? html`<${Dock}
          barRef=${barRef}
          on=${entries.length > 0}
          count=${linkCount}
          asks=${dockQuality(chosenValues, sendable.map(probedOf))}
          label=${label}
          disabled=${busy || sendable.length === 0}
          busy=${busy}
          title=${sendable.length === 0 && entries.length ? 'No checked link can be downloaded' : undefined}
          onSubmit=${() => submit()}
          failure=${failure}
          refusal=${refusal}
          onCustom=${() => setCustomOpen(true)}
        />`
      : null}
    ${results.rows.length
      ? html`<${ResultsCard}
          rows=${results.rows}
          params=${route.params}
          busy=${retrying}
          onRetry=${retry}
          onClear=${clearResults}
        />`
      : null}
    ${docked ? null : html`<${BrowserShortcut} />`}
    ${customOpen && defaults
      ? html`<${CustomSheet}
          defaults=${defaults}
          start=${presetValues(defaults, choice.preset, choice.custom)}
          onDone=${(custom) => {
            setChoice({ preset: 'custom', custom });
            closeCustom();
          }}
          onFollow=${() => {
            setChoice({ preset: 'follow', custom: choice.custom });
            closeCustom();
          }}
          onClose=${closeCustom}
        />`
      : null}
    ${quality
      ? html`<${QualitySheet}
          url=${quality.url}
          title=${quality.title}
          tracks=${quality.tracks}
          onProbed=${() => setProbed((n) => n + 1)}
          onClose=${() => setQuality(null)}
        />`
      : null}
    ${confirm ? html`<${ArtistConfirm} confirm=${confirm} onClose=${() => setConfirm(null)} />` : null}
    ${asking
      ? html`<${AskDialog}
          title=${asking.title}
          asks=${asking.asks}
          onDone=${(answer) => {
            setAsking(null);
            asking.go(answer);
          }}
          onCancel=${() => setAsking(null)}
        />`
      : null}
  </div>`;
}
