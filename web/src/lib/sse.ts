// A small Server-Sent Events parser for fetch() streams (the live tail can't use
// EventSource: it can't send the X-Woodshed-Session header). It follows the
// WHATWG "event stream interpretation" rules for what the live tail needs: lines end in
// LF, CRLF or CR; "event", "data", "id" and "retry" fields; ":" comment lines
// (heartbeats); a blank line dispatches. Unknown fields are ignored.

export interface SseEvent {
  event: string;
  data: string;
  id: string;
}

export class SseParser {
  private buf = '';
  private event = '';
  private data: string[] = [];
  private id = '';
  private sawCR = false;
  /** Set by a "retry:" field (milliseconds). */
  retry: number | null = null;

  private readonly onEvent: (e: SseEvent) => void;

  constructor(onEvent: (e: SseEvent) => void) {
    this.onEvent = onEvent;
  }

  /** Feeds decoded text; any chunk boundary is fine, including inside a CRLF. */
  push(chunk: string): void {
    let text = chunk;
    // A CR at the end of the last chunk followed by LF here is one line ending.
    if (this.sawCR && text.startsWith('\n')) text = text.slice(1);
    this.sawCR = false;
    let start = 0;
    for (let i = 0; i < text.length; i++) {
      const c = text.charCodeAt(i);
      if (c === 10 || c === 13) {
        this.line(this.buf + text.slice(start, i));
        this.buf = '';
        if (c === 13) {
          if (i + 1 < text.length) {
            if (text.charCodeAt(i + 1) === 10) i++;
          } else this.sawCR = true;
        }
        start = i + 1;
      }
    }
    this.buf += text.slice(start);
    // A line can't grow without bound if a broken server never sends a line ending.
    if (this.buf.length > 4 * 1024 * 1024) this.buf = '';
  }

  private line(l: string): void {
    if (l === '') {
      if (this.data.length) this.onEvent({ event: this.event || 'message', data: this.data.join('\n'), id: this.id });
      this.event = '';
      this.data = [];
      return;
    }
    if (l.startsWith(':')) return; // comment: a heartbeat
    const colon = l.indexOf(':');
    const field = colon < 0 ? l : l.slice(0, colon);
    let value = colon < 0 ? '' : l.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') this.event = value;
    else if (field === 'data') this.data.push(value);
    else if (field === 'id') {
      if (!value.includes('\0')) this.id = value;
    } else if (field === 'retry' && /^\d+$/.test(value)) this.retry = Number(value);
  }
}
