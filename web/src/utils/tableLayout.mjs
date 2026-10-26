export const tableTextWidths = Object.freeze({ collapsed: 170, expanded: 236 })

export function sumTableColumnWidths(columns = [], selectionWidth = 0) {
  return (Array.isArray(columns) ? columns : []).reduce((total, column) => {
    const width = Number(column?.width)
    return total + (Number.isFinite(width) && width > 0 ? width : 0)
  }, Math.max(0, Number(selectionWidth) || 0))
}

export function tableExpansionKey(rowId, field) {
  return JSON.stringify([String(rowId), String(field)])
}
