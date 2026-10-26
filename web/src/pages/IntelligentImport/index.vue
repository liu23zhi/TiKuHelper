<template>
  <div class="console-page import-page">
    <ConsolePageHeading title="智能导入" description="上传文档、识别题目，校对后批量写入题库。">
      <template #actions><a-space wrap class="section-actions import-page-actions">
        <a-button type="primary" @click="upload" :loading="submitLoading">确认导入</a-button>
        <a-button @click="navigateToHome">返回</a-button>
      </a-space></template>
    </ConsolePageHeading>

    <main class="import-workspace">
    <ConsolePanel class="compileBox" title="题目编辑" description="上传 .docx 文档后，按题目格式校对识别内容。">
      <template #actions>
          <a-upload name="file" action="/sqp/api/upload" @change="handleChange" :showUploadList="false"
            :beforeUpload="beforeUpload" accept=".docx">
            <a-button>
              <upload-outlined></upload-outlined>
              上传文件
            </a-button>
          </a-upload>
      </template>
      <div class="info">
        提示：若识别有误，可点击左侧题目按格式进行修改后重新识别
      </div>
      <a-spin :spinning="loading">
        <template v-if="showProblemArr.length > 0">
          <div class="compileContent" ref="compileContent" @scroll="handleScroll">
            <div v-for="(i, index) in showProblemArr" :key="index" contenteditable='true'
              :class="type == '全部' ? '' : type == i.typeAlias ? '' : 'display'">
              <p class="line" v-if="i.typeAlias"> 【{{ i.typeAlias }}】</p>
              <p class="lines" v-if="i.content">{{ index + 1 }}. {{ i.content }} </p>
              <template v-if="i.options">
                <p class="lines" v-for="(val, key) in i.options" :key="key">
                  {{ val.name }}.{{ val.value }}
                </p>
              </template>
              <p v-if="i.hintMsg" class="line red">{{ i.hintMsg }}</p>
              <p class="lines" v-if="i.answer">
                正确答案：{{ i.answer.join('') }}
              </p>
            </div>
          </div>
        </template>

        <div v-else class="explain">
          <p>录入说明：</p>
          <p>1. 题号：最好有题号，题号用数字表示，如“1.”、“2.”、“3.”。如无题号需在题目与题目之间增加空行。</p>
          <p>2.
            题型：支持导入单选题、多选题、填空题、判断题、简答题、阅读理解、完形填空题型，若要导入其他题型请用【题型名称】如【名词解释】、【计算题】在题干前标注不同的题型，否则会识别为简答题。</p>
          <p>3. 答案：可在题干或题干+选项下方另起一行用“答案：”“参考答案：”标出。示例：答案：XXX</p>
          <p class="region">编辑区</p>
        </div>
      </a-spin>
    </ConsolePanel>
    <div class="handleBox">
      <a-button @click="handleInput()" type="primary">
        重新识别
      </a-button>
      <a-button @click="() => { problemArr = []; showProblemArr = []; data = [] }">
        清空题目
      </a-button>
    </div>
    <ConsolePanel class="previewBox" title="题目预览" description="检查题型、选项和答案，确认后导入题库。">
      <template #actions><a-tag v-if="data.length > 0" color="cyan">共识别 {{ data.length }} 题</a-tag></template>
      <div v-if="problemArr.length" class="tabs" role="group" aria-label="预览题型筛选">
        <button v-for="(i, index) in problemArr" :key="index" type="button" :aria-pressed="type === i.typeAlias" @click="() => { type = i.typeAlias }"
          :class="type === i.typeAlias ? 'active tabsBtn' : 'tabsBtn'">
          {{ i.typeAlias || '其他' }}({{ i.paperList.length }})
        </button>
      </div>
      <a-spin :spinning="loading">
        <template v-if="showProblemArr.length > 0">
          <div class="previewContent" ref="previewContent" @scroll="handleScroll">
            <div v-for="(i, index) in showProblemArr" :key="index"
              :class="type === '全部' ? '' : type === i.typeAlias ? '' : 'display'">
              <p class="line" v-if="i.typeAlias"> 【{{ i.typeAlias }}】</p>
              <p class="lines" v-if="i.content">{{ index + 1 }}. {{ i.content }} </p>
              <template v-if="i.options">
                <p class="lines" v-for="(val, key) in i.options" :key="key">
                  {{ val.name }}.{{ val.value }}
                </p>
              </template>
              <p v-if="i.hintMsg" class="line red">{{ i.hintMsg }}</p>
              <p class="lines green" v-if="i.answer">
                正确答案：{{ i.answer.join(',') }}
              </p>
            </div>
          </div>
        </template>
        <div v-else class="explain">
          <p class="region">预览区</p>
        </div>
      </a-spin>

    </ConsolePanel>
  </main>
  </div>
</template>

<script>
import { message } from 'ant-design-vue';
import { useRouter } from 'vue-router';
import { UploadOutlined } from '@ant-design/icons-vue';
import { defineComponent, ref } from 'vue';
import {
  parseFile,
  reParseFile
} from '@/api/api'
import { createQuestions } from '@/api/TikuComponentApi'
import { getQuestionTypeByName } from "@/utils/uitls";

const UploadId = ref('');
const problemArr = ref([]);
const showProblemArr = ref([]);
const loading = ref(false);
const data = ref([])
const type = ref('全部')
const submitLoading = ref(false)

export default defineComponent({
  name: 'IntelligentImport',
  components: {
    UploadOutlined,
  },
  setup() {
    const router = useRouter();
    const navigateToHome = () => {
      router.push('/adapter/component');
    };
    return {
      UploadId,
      problemArr,
      showProblemArr,
      loading,
      data,
      type,
      navigateToHome,
      submitLoading
    };
  },
  methods: {
    // 获取解析文件
    async getParseFile() {
      const res = await parseFile({
        file: UploadId.value
      })
      const { paper } = res
      if (paper.length > 0) {
        this.processPaperData(paper);
        data.value = paper;
        loading.value = false;
      } else {
        loading.value = false;
      }
    },
    // 重新解析文件
    async reParseFile(val) {
      problemArr.value = [];
      showProblemArr.value = [];
      data.value = []
      type.value = '全部'
      loading.value = true;
      const res = await reParseFile({
        html: val
      })
      const { paper } = res
      if (paper.length > 0) {
        this.processPaperData(paper);
        data.value = paper;
        loading.value = false;
      } else {
        loading.value = false;
      }
    },
    processPaperData(paper) {
      showProblemArr.value = paper;
      console.log(showProblemArr.value);
      const problemType = [...new Set(paper.map(item => item.typeAlias))];
      const Arr = [
        { typeAlias: '全部', paperList: paper },
        ...problemType.map(item => ({ typeAlias: item, paperList: paper.filter(i => i.typeAlias === item) }))
      ];
      problemArr.value = Arr;
    },
    handleChange(info) {
      if (info.file.status !== 'uploading') {
        UploadId.value = info.fileList[0].response.crc;
        this.getParseFile();
      }
      if (info.file.status === 'done') {
        message.success(`${info.file.name} 上传成功`);
      } else if (info.file.status === 'error') {
        message.error(`${info.file.name} 上传失败`);
      }
    },
    beforeUpload() {
      message.success(`文件开始上传，请耐心等待...`);
      loading.value = true;
    },
    handleScroll(event) {
      const scrollTop = event.target.scrollTop;
      if (event.target === this.$refs.compileContent) {
        this.$refs.previewContent.scrollTop = scrollTop;
      } else if (event.target === this.$refs.previewContent) {
        this.$refs.compileContent.scrollTop = scrollTop;
      }
    },
    handleInput() {
      if (showProblemArr.value.length === 0) return;
      const lines = document.querySelectorAll('.compileContent .lines');
      const linesArr = Array.from(lines).map(item => item.innerHTML);
      const html = linesArr.map(item => `<p>${item}</p>`).join('');
      this.reParseFile(html);
    },
    upload() {
      const result = data.value.map(item => {
        const data = {
          question: item.content,
          type: getQuestionTypeByName(item.typeAlias),
        }
        if (item.options) {
          data.options = item.options.map(i => {
            return i.value
          })
        }
        if (item.answer) {
          if (data?.options && data.options.length > 0) {
            data.answer = JSON.stringify(item.answer.map(i => {
              return data.options[i.charCodeAt(0) - 65]
            }))
          } else {
            data.answer = JSON.stringify(item.answer)
          }
        }
        data.options = JSON.stringify(data.options)
        return data
      }).filter(i => i.answer && i.answer.length > 0)
      submitLoading.value = true
      createQuestions(result).then(res => {
        message.success(res.message);
        submitLoading.value = false
      }).catch(err => {
        submitLoading.value = false
        message.error(err.message)
      })
    }
  }
});
</script>
<style scoped>
.import-workspace { display: grid; grid-template-columns: minmax(0, 1fr) 108px minmax(0, 1fr); gap: 16px; align-items: stretch; min-width: 0; }
.compileBox, .previewBox { min-width: 0; }
.handleBox { display: flex; flex-direction: column; justify-content: center; gap: 12px; }
.info { margin-bottom: 16px; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.tabs { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 16px; }
.tabsBtn { padding: 6px 10px; border: 1px solid var(--console-border); border-radius: 6px; background: var(--console-surface); color: var(--console-muted); cursor: pointer; font: inherit; font-size: 13px; }
.tabsBtn:hover, .tabsBtn.active { border-color: var(--console-primary); color: var(--console-primary); background: var(--console-selected); }
.compileContent, .previewContent { min-height: 360px; max-height: 65vh; overflow: auto; padding: 8px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-subtle); overflow-wrap: anywhere; }
.compileContent > div, .previewContent > div { padding: 12px; border-radius: 6px; }
.compileContent > div + div, .previewContent > div + div { margin-top: 8px; }
.compileContent > div { background: var(--console-surface); border: 1px solid var(--console-border); }
.previewContent > div:hover { background: var(--console-selected); }
.compileContent [contenteditable]:focus-visible { outline: 2px solid var(--console-primary); outline-offset: 1px; }
.line, .lines { display: block; margin: 0 0 8px; line-height: 1.8; white-space: pre-wrap; }
.green { color: var(--console-success); }
.red { color: var(--console-danger); }
.explain { min-height: 360px; padding: 20px; border: 1px dashed var(--console-border); border-radius: 8px; background: var(--console-subtle); }
.explain p { color: var(--console-muted); font-size: 13px; line-height: 1.9; }
.region { margin: 32px 0 0; text-align: center; font-size: 24px !important; color: var(--console-muted); }
.display { display: none; }
@media (max-width: 1024px) { .import-workspace { grid-template-columns: minmax(0, 1fr); } .handleBox { flex-direction: row; justify-content: flex-start; flex-wrap: wrap; } .compileContent, .previewContent { min-height: 280px; max-height: 50vh; } .explain { min-height: 240px; } }
</style>
