import test from 'node:test'
import assert from 'node:assert/strict'
import { effectScope, nextTick, ref } from 'vue'
import { useTableExpansion } from '../composables/useTableExpansion.mjs'
import { sumTableColumnWidths, tableExpansionKey } from './tableLayout.mjs'

test('scroll width includes explicit columns and the selection column', () => {
  assert.equal(sumTableColumnWidths([{ width: 90 }, { width: 170 }, { width: 180 }], 48), 488)
  assert.equal(sumTableColumnWidths([{ width: 90 }, { width: 236 }, { width: 180 }], 48), 554)
  assert.notEqual(tableExpansionKey('a:b', 'c'), tableExpansionKey('a', 'b:c'))
})

test('expansion is independent per cell and a column shrinks only after its last expanded cell closes', async () => {
  const scope = effectScope()
  try {
    const rows = ref([{ id: 1 }, { id: 2 }])
    const expansion = scope.run(() => useTableExpansion(rows))
    assert.equal(expansion.columnWidth('answer'), 170)
    expansion.toggle(1, 'answer')
    expansion.toggle(rows.value[1], 'answer')
    expansion.toggle(1, 'question')
    expansion.toggle(1, 'answer')
    assert.equal(expansion.columnWidth('answer'), 236)
    assert.equal(expansion.isExpanded(1, 'answer'), false)
    assert.equal(expansion.columnWidth('question'), 236)
    expansion.toggle(2, 'answer')
    assert.equal(expansion.columnWidth('answer'), 170)
    rows.value = [{ id: 1 }]
    await nextTick()
    assert.equal(expansion.isExpanded(1, 'question'), true)
    rows.value = [{ id: 3 }]
    await nextTick()
    assert.equal(expansion.columnWidth('question'), 170)
    assert.equal(expansion.isExpanded(1, 'question'), false)
  } finally { scope.stop() }
})
