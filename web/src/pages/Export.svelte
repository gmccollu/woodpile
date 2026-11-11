<script lang="ts">
  import ComingSoon from '../components/ComingSoon.svelte';
</script>

<ComingSoon
  title="Export"
  intro="Package logs as a zip to send to someone else."
  points={[
    'Pick devices and a time range',
    'One plain-text file per device per day, decompressed so anyone can open it',
    "A manifest with line counts, checksums and any gaps (for example while the listener was down)",
    "Woodpile's own events for the range, so the recipient can tell 'the device sent nothing' from 'Woodpile wasn't running'",
    'Recent exports stay here for download for 24 hours',
  ]}
/>
