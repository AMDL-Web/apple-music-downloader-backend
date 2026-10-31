// A running job's average speed and time left (lib/speed.js), kept in the page
// from the updates it already gets: the drawer samples its own job's overall
// progress, the Overview's live queue samples the feed's.

import { createContext, useEffect, useRef, useState } from './preact.js';
import { useClock } from './hooks.js';
import { getDownload } from '../api.js';
import { addSample, createRate, remainingMs, avgSpeed, speedWords } from '../lib/speed.js';

/**
 * The drawer's words for one job: {avg, left} ("avg 41 MB/s", "~1 min left"),
 * each '' when unknown. `fraction` is the job's unrounded overall progress
 * (lib/album-progress.js); the time left needs a few seconds of it moving.
 * A finished job keeps its average only when it completed.
 */
export function useJobRate(job, items, fraction) {
  const running = job.status === 'running';
  useClock(running ? 2000 : 0);
  const key = `${job.id}:${job.started_at || ''}`;
  const box = useRef({ key: '', rate: createRate() });
  if (box.current.key !== key) box.current = { key, rate: createRate() };
  useEffect(() => {
    if (running) addSample(box.current.rate, Date.now(), fraction);
  }, [key, running, fraction]);
  if (running) return speedWords(avgSpeed(job, items, Date.now()), remainingMs(box.current.rate));
  return speedWords(job.status === 'completed' ? avgSpeed(job, items, 0) : 0, 0);
}

/** The Overview's live cards read their words from here: {[job id]: {avg, left}} in bytes per second and milliseconds. */
export const RatesContext = createContext(null);

// The feed's job payloads carry the overall progress but no tracks, so the
// bytes behind the average come from one read of the job per this long.
const READ_GAP_MS = 4000;

/**
 * Samples every running job of the list each time the list changes (the feed
 * pushes a running job's progress every 2 s while it moves) and reads the
 * job's tracks for the bytes moved. Returns the RatesContext value.
 */
export function useLiveRates(jobs) {
  const [rates, setRates] = useState({});
  const book = useRef(new Map());
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    const publish = () => {
      if (!alive.current) return;
      const next = {};
      for (const [id, e] of book.current) next[id] = { avg: e.avg, left: remainingMs(e.rate) };
      setRates(next);
    };
    const now = Date.now();
    const running = jobs.filter((j) => j.status === 'running');
    for (const id of [...book.current.keys()]) if (!running.some((j) => j.id === id)) book.current.delete(id);
    for (const job of running) {
      let e = book.current.get(job.id);
      // A retry starts a new run: the old samples and bytes belong to the last one.
      if (!e || e.started !== (job.started_at || '')) {
        e = { started: job.started_at || '', rate: createRate(), avg: 0, readAt: 0, busy: false };
        book.current.set(job.id, e);
      }
      if (typeof job.progress === 'number') addSample(e.rate, now, job.progress);
      if (!e.busy && now - e.readAt >= READ_GAP_MS) {
        e.busy = true;
        e.readAt = now;
        getDownload(job.id).then(
          (body) => {
            e.busy = false;
            if (book.current.get(job.id) !== e || !body.job || body.job.status !== 'running') return;
            e.avg = avgSpeed(body.job, body.items, Date.now());
            publish();
          },
          () => {
            e.busy = false;
          },
        );
      }
    }
    publish();
  }, [jobs]);
  return rates;
}
