// portal moves an element to <body> while it is mounted (Svelte action), so a
// modal opened inside a table cell is not dimmed or clipped by the row (an
// opacity < 1 makes its own stacking context) and covers the whole window.
export function portal(node) {
  document.body.appendChild(node)
  return {
    destroy() {
      node.remove()
    },
  }
}
