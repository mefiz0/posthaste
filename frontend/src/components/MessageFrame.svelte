<script lang="ts">
  import { buildFrameDocument, resolveCids } from '../lib/msghtml';

  let {
    html,
    allowRemote = false,
    resolveCid,
  }: {
    html: string;
    allowRemote?: boolean;
    resolveCid: (cid: string) => Promise<string | null>;
  } = $props();

  let doc = $state('');

  $effect(() => {
    const source = html;
    const remote = allowRemote;
    let cancelled = false;
    void resolveCids(source, resolveCid).then((resolved) => {
      if (!cancelled) doc = buildFrameDocument(resolved, { allowRemote: remote });
    });
    return () => {
      cancelled = true;
    };
  });
</script>

<!--
  sandbox="" grants no tokens at all: no scripts, no same-origin, no
  navigation. The document additionally carries a script-src 'none' CSP as
  defense in depth; this iframe is the only place message HTML is rendered.
-->
<iframe class="msg-frame" title="Message content" sandbox="" srcdoc={doc}></iframe>
