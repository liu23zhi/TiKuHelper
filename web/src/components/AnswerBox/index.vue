<template>
  <div class="answer-editor">
  <div v-if="data.length">
    <a-row v-for="(item, index) in data" :key="item.id" class="rowStyle">
      <a-col :span="20">
        <label :for="item.id" class="answer-label">答案 {{ index + 1 }}</label>
        <a-textarea :id="item.id" :ref="input => setAnswerInput(item.id, input)" v-model:value="item.value" />
      </a-col>
      <a-col :span="4">
        <a-button v-if="index >= 0" @click="addOption" type="link" html-type="button" aria-label="添加答案" title="添加答案" primary>
          <template #icon>
            <PlusCircleOutlined />
          </template>
        </a-button>
        <a-button v-if="index >= 0" @click="removeOption(index)" type="link" html-type="button" :aria-label="'删除第 ' + (index + 1) + ' 条答案'" :title="'删除第 ' + (index + 1) + ' 条答案'" danger>
          <template #icon>
            <MinusCircleOutlined />
          </template>
        </a-button>
      </a-col>
    </a-row>
  </div>
  <div v-else>
    <a-button ref="emptyAddButton" class="addBut" @click="addOption" html-type="button" aria-label="添加答案" style="width: 100%">
      添加
    </a-button>
  </div>
  <span class="answer-status" role="status" aria-live="polite" aria-atomic="true">{{ announcement }}</span>
  </div>
</template>

<script>
import { getCurrentInstance, nextTick, onBeforeUnmount, ref } from 'vue'
import { PlusCircleOutlined, MinusCircleOutlined } from '@ant-design/icons-vue'

export default {
  name: 'AnswerBox',
  components: { PlusCircleOutlined, MinusCircleOutlined },
  props: { answer: { type: Array, default: () => [] } },
  setup(props) {
    const prefix = 'answer-box-' + getCurrentInstance().uid
    let nextRow = 0
    let alive = true
    const makeRow = value => ({ id: prefix + '-row-' + nextRow++, value })
    const data = ref(props.answer.map(makeRow))
    const inputRefs = new Map()
    const emptyAddButton = ref(null)
    const announcement = ref('')
    const setAnswerInput = (id, input) => {
      if (input) inputRefs.set(id, input)
      else inputRefs.delete(id)
    }
    const focusRow = (id) => {
      nextTick(() => {
        if (!alive) return
        const input = inputRefs.get(id)
        if (typeof input?.focus === 'function') input.focus()
        else input?.$el?.focus()
      })
    }
    const addOption = () => {
      const row = makeRow('')
      data.value.push(row)
      announcement.value = '已添加答案 ' + data.value.length + '，共 ' + data.value.length + ' 条'
      focusRow(row.id)
    }
    const removeOption = (index) => {
      if (index < 0 || index >= data.value.length) return
      data.value.splice(index, 1)
      announcement.value = '已删除答案 ' + (index + 1) + '，剩余 ' + data.value.length + ' 条'
      const target = data.value[index] || data.value[data.value.length - 1]
      if (target) focusRow(target.id)
      else nextTick(() => {
        if (alive) (emptyAddButton.value?.$el || emptyAddButton.value)?.focus()
      })
    }
    // Upload/import calls must keep focus where the user is working.
    const pushAnswer = (str) => { data.value.push(makeRow(str)) }
    // Preserve the public JSON contract, ordering, and repeated non-objective answers.
    const getData = () => JSON.stringify(data.value.map((item) => {
      if (item.value !== null && item.value !== undefined) return item.value
    }).filter(value => value))
    onBeforeUnmount(() => { alive = false; inputRefs.clear() })
    return { data, emptyAddButton, announcement, setAnswerInput, addOption, removeOption, pushAnswer, getData }
  },
}
</script>

<style scoped>
.rowStyle {
  margin-bottom: 10px;
}

.CheckStyle {
  width: 30px;
  height: 30px;
  line-height: 28px;
  text-align: center;
  border-radius: 50%;
  cursor: pointer;
  font-size: 16px;
  box-sizing: border-box;
}

/* 选中的样式 */
.Check {
  background-color: var(--primary);
  color: var(--surface);
  border: 1px solid var(--primary);
}

/* 未选中的样式 */
.CheckNo {
  background-color: var(--surface);
  color: var(--primary);
  border: 1px solid var(--primary);
  box-sizing: border-box;
}

.addBut {
  color: rgba(0,0,0,.65);
  background-color: var(--surface);
  border-color: #d9d9d9;
  border-style: dashed;
  padding: 0 15px;
  font-size: 14px;
  border-radius: 4px;
  height: 32px;
}

.addBut:focus{
  color: #108ee9;
  background-color: var(--surface);
  border-color: #108ee9;
}

.addBut:hover{
  color: #108ee9;
  background-color: var(--surface);
  border-color: #108ee9;
}
/* Authored accessible labels/live updates do not change the existing row grid. */
.answer-label,
.answer-status {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}
</style>