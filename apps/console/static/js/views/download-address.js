// New download: the address and the text box follow each other. Links in
// ?url= join the text, a term in ?q= fills a box without links, and the
// settled text writes its links into ?url=, its search term into ?q= and
// keeps ?artist= while the term it was found with stays, and ?group= while
// there is a term.

import { useEffect, useRef, useState } from '../ui/preact.js';
import { newDownloadUrls, buildHash } from '../lib/route.js';
import { scanLinks, mergeLinks, openingText } from '../lib/links.js';
import { searchTerm } from '../lib/search.js';

const sameList = (a, b) => a.length === b.length && a.every((x, i) => x === b[i]);

// The text box outlives the page for the life of the tab, so leaving New
// download through the sidebar and coming back keeps a draft, notes and
// links that are not Apple Music included (?url= keeps only the links).
let draft = '';

/**
 * The text box, kept as the tab's draft: [text, setText]. Links brought
 * from another page start a new download (lib/links.js openingText); when
 * that replaces a draft, a toast offers Undo, which brings the draft back
 * with the box's links joined to it (into the draft alone once the page has
 * gone).
 */
export function useDraft({ app, incoming, storefront }) {
  const [start] = useState(() => {
    const text = openingText(draft, incoming, storefront);
    return { text, replaced: text !== draft && draft.trim() ? draft : '' };
  });
  const [text, setText] = useState(start.text);
  draft = text;
  const alive = useRef(true);
  useEffect(() => {
    if (start.replaced) {
      const undo = () => {
        const next = mergeLinks(start.replaced, scanLinks(draft, storefront).map((e) => e.url), storefront);
        if (alive.current) setText(next);
        else draft = next;
      };
      app.toast({ message: 'Replaced your earlier draft', action: { label: 'Undo', run: undo } });
    }
    return () => {
      alive.current = false;
    };
  }, []);
  return [text, setText];
}

/**
 * ?q= -> text: a term from the address (the palette's search, a reload)
 * replaces a box that holds no link, through `edit`; with links in the box
 * a toast says why it did not. Returns a ref the page sets to each term it
 * writes into ?q= itself, which is already in the box.
 */
function useTermParam({ app, qParam, text, storefront, edit }) {
  const wrote = useRef('');
  const textRef = useRef(text);
  textRef.current = text;
  useEffect(() => {
    if (!qParam || qParam === wrote.current) return;
    const now = textRef.current;
    if (scanLinks(now, storefront).length) {
      app.toast({ message: 'The box holds links: download or clear them to search', role: 'warning' });
      return;
    }
    if (searchTerm(now, []).term !== qParam) edit(qParam);
  }, [qParam]);
  return wrote;
}

/**
 * Keeps the address and the text box in step; `incoming` are the links in
 * ?url=, `settled` the debounced text, `edit` the page's text setter.
 * Returns a ref to the current route.
 */
export function useAddressSync({ app, route, incoming, text, settled, storefront, edit }) {
  // ?url= -> text: links that arrive in the address (the global paste on
  // this page, a bookmarklet, a shared link) join the text.
  const urlParam = route.params.url || '';
  useEffect(() => {
    if (incoming.length) edit((t) => mergeLinks(t, incoming, storefront));
  }, [urlParam]);

  // ?q= -> text.
  const wroteQ = useTermParam({ app, qParam: route.params.q || '', text, storefront, edit });

  // text -> ?url=: the links follow the text, so a reload keeps them, also
  // after the address lost them (n, the sidebar and the palette open bare
  // #/new). When only the address changed and it holds links the text does
  // not, the effect above is merging them; the next settled text writes
  // the address.
  const routeRef = useRef(route);
  routeRef.current = route;
  const settledSeen = useRef(settled);
  useEffect(() => {
    const r = routeRef.current;
    const found = scanLinks(settled, storefront);
    const urls = found.map((e) => e.url);
    const q = searchTerm(settled, found).term;
    // An open artist belongs to the term it was found with.
    const artist = q && q === (r.params.q || '') ? r.params.artist : undefined;
    // A group shown alone stays as the words change.
    const group = q ? r.params.group : undefined;
    const inParam = newDownloadUrls(r.params);
    const textMoved = settledSeen.current !== settled;
    settledSeen.current = settled;
    if (sameList(urls, inParam) && q === (r.params.q || '') && artist === r.params.artist && group === r.params.group) return;
    if (!textMoved && scanLinks(inParam.join('\n'), storefront).some((e) => !urls.includes(e.url))) return;
    if (!textMoved && r.params.q && r.params.q !== q) return;
    wroteQ.current = q;
    app.navigate(buildHash('new', { ...r.params, url: urls.join('\n'), q, artist, group }, r.jobId), { replace: true });
  }, [settled, urlParam, route.params.q, storefront]);
  return routeRef;
}
