<script lang="ts">
  import Icon from './Icon.svelte';
  let { text, label = 'Copy' }: { text: string; label?: string } = $props();
  let done = $state(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // Clipboard API needs a secure context; fall back to a hidden textarea.
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
    }
    done = true;
    setTimeout(() => (done = false), 1500);
  }
</script>

<button type="button" class="ghost-btn" onclick={copy} aria-label="{label}: {text}">
  <Icon name={done ? 'check' : 'copy'} size={15} />
  <span aria-live="polite">{done ? 'Copied' : label}</span>
</button>
