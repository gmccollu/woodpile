<script lang="ts">
  import ComingSoon from '../components/ComingSoon.svelte';
</script>

<ComingSoon
  title="Search"
  intro="Search every stored log, including the compressed days."
  points={[
    'Text search (case-insensitive), or a regular expression',
    'Filter by device, group, time range and severity',
    'Results stream in as they are found, up to 10,000 at a time, with Load more',
    'Open any hit with the 50 lines before and after it',
  ]}
/>
