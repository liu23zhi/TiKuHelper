<template>
  <div class="console-page import-page">
    <header class="section-heading import-heading">
      <div>
        <h1>智能导入</h1>
        <p class="page-lead">识别题目内容，校对后批量写入题库。</p>
      </div>
      <a-space wrap class="section-actions import-page-actions">
        <a-button type="primary" @click="upload" :loading="submitLoading">确认导入</a-button>
        <a-button @click="navigateToHome">返回</a-button>
      </a-space>
    </header>

    <main class="import-workspace">
    <section class="compileBox console-panel">
      <div class="title">
        <div>
          题目编辑
        </div>
        <div>
          <a-upload name="file" action="/sqp/api/upload" @change="handleChange" :showUploadList="false"
            :beforeUpload="beforeUpload" accept=".docx">
            <a-button>
              <upload-outlined></upload-outlined>
              上传文件
            </a-button>
          </a-upload>
        </div>
      </div>
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
    </section>
    <div class="handleBox">
      <a-button @click="handleInput()" type="primary">
        重新识别
      </a-button>
      <a-button @click="() => { problemArr = []; showProblemArr = []; data = [] }">
        清空题目
      </a-button>
    </div>
    <section class="previewBox console-panel">
      <div class="title">
        <div>
          题目预览
        </div>
        <div class="overview" v-if="data.length > 0">
          共识别
          <span>{{ data.length }}</span>
          题。
        </div>
      </div>
      <div class="tabs">
        <span v-for="(i, index) in problemArr" :key="index" @click="() => { type = i.typeAlias }"
          :class="type === i.typeAlias ? 'active tabsBtn' : 'tabsBtn'">
          {{ i.typeAlias || '其他' }}({{ i.paperList.length }})
        </span>
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

    </section>
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
.console-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
  color: var(--console-text, #1f2937);
}

.import-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.import-heading h1 {
  margin: 0;
  color: var(--console-text, #1f2937);
  font-size: 20px;
  font-weight: 650;
}

.page-lead {
  margin: 4px 0 0;
  color: var(--console-muted, #64748b);
  font-size: 13px;
}

main {
  width: 100%;
  display: flex;
  flex: 1;
  align-items: stretch;
  min-width: 0;
  min-height: 400px;
}

.import-workspace {
  display: flex;
  align-items: stretch;
  gap: 16px;
  min-width: 0;
}

.compileBox {
  flex: 1 1 0;
  width: 0;
  min-height: 400px;
  padding: 16px;
  border-color: var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
  background-color: var(--console-surface, #fff);
}

.handleBox {
  flex: 0 0 120px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 12px;
  min-width: 0;
}

.previewBox {
  flex: 1 1 0;
  width: 0;
  min-height: 400px;
  padding: 16px;
  border-color: var(--console-border, #e5e7eb);
  border-radius: var(--console-radius, 12px);
  background-color: var(--console-surface, #fff);
}

.title {
  font-size: 18px;
  font-weight: 600;
  margin-bottom: 12px;
  display: flex;
  justify-content: space-between;
  height: 30px;
  align-items: center;
}

.title .overview {
  height: 30px;
  font-size: 12px;
  padding: 8px 20px;
  box-sizing: border-box;
  font-weight: 400;
  text-align: left;
  border-radius: 8px;
  background-color: #f0f2f5;
}

.overview>span:nth-child(1) {
  color: #00B368;
  font-weight: 500;
}

.overview>span:nth-child(2) {
  color: #F33131;
  font-weight: 500;
}

.info {
  height: 36px;
  display: flex;
  align-items: center;
}

.tabs {
  height: 36px;
}

.tabsBtn {
  cursor: pointer;
  height: 30px;
  line-height: 30px;
  display: inline-block;
  border-radius: 4px;
  padding: 0 10px;
  color: #000;
  box-sizing: border-box;
}

.active {
  background-color: #195ca3;
  color: #fff;
}

.compileContent {
  border: 1px solid #e8e8e8;
  max-height: calc(100vh - 120px - 92px - 60px);
  min-height: 200px;
  border-radius: 4px;
  padding: 10px;
  overflow: hidden;
  overflow-y: scroll;
  box-sizing: border-box;
  color: #A8A8B3;

}

.compileContent>div {
  padding: 10px;
  border-radius: 4px;
}

.previewContent {
  border: 1px solid #e8e8e8;
  max-height: calc(100vh - 120px - 92px - 60px);
  min-height: 200px;
  border-radius: 4px;
  padding: 10px;
  overflow: hidden;
  overflow-y: scroll;
  box-sizing: border-box;
}

.previewContent>div {
  padding: 10px;
  border-radius: 6px;
}

.previewContent>div:hover {
  background-color: #f0f2f5;
}

:focus-visible {
  outline-color: transparent;
  outline-style: solid;
  outline-width: 1px;
}

:focus {
  box-shadow: 0 0 4px 1px rgba(58, 139, 255, 0.5);
  color: #333;
}

.line {
  font-size: 14px;
  line-height: 24px;
  /* color: #333; */
  display: block;

}

.lines {
  font-size: 14px;
  line-height: 24px;
  /* color: #333; */
  display: block;

}


.green {
  color: #00B368;
}

.red {
  color: #F33131;
}

.explain {
  height: calc(100vh - 120px - 92px - 60px);
  overflow: hidden;
  border: 1px solid #e3e3e3;
  border-radius: 6px;
  padding: 10px;
  position: relative;
}

.explain p {
  font-size: 14px;
  color: #acb4bf;
  margin-bottom: 10px;
}

.region {
  font-size: 28px !important;
  font-weight: 600;
  color: #acb4bf;
}

.display {
  display: none;
}

@media (max-width: 900px) {
  .import-workspace {
    flex-direction: column;
  }

  main {
    flex-direction: column;
  }

  .compileBox,
  .previewBox {
    flex: none;
    width: 100%;
    min-height: 360px;
  }

  .handleBox {
    flex-direction: row;
    flex: none;
    width: 100%;
  }

  .handleBox :deep(.ant-btn) {
    flex: 1;
  }
}

@media (max-width: 600px) {
  .import-heading,
  .import-page-actions {
    width: 100%;
  }

  .compileBox,
  .previewBox {
    min-height: 300px;
    padding: 12px;
  }

  .title {
    height: auto;
    min-height: 30px;
  }

  .tabs {
    height: auto;
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
}
</style>
