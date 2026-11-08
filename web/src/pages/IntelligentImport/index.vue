<template>
  <div v-if="pageMounted" :key="pageKey" ref="pageSurface" class="console-page import-page" :onInputCapture="pageEntry.guardNative" :onChangeCapture="pageEntry.guardNative" :onClickCapture="pageEntry.guardNative">
    <ConsolePageHeading title="智能导入" description="上传文档、识别题目，校对后批量写入题库。">
      <template #actions><a-space wrap class="section-actions import-page-actions">
        <a-button type="primary" :onClick="pageEntry.bind(upload)" :loading="submitLoading" :disabled="!canImport() || loading">确认导入</a-button>
        <a-button :onClick="pageEntry.bind(navigateToHome)">返回</a-button>
      </a-space></template>
    </ConsolePageHeading>

    <a-alert v-if="!canImport()" type="warning" show-icon class="page-alert" message="当前身份尚未确认或无题库管理权限，请重新登录管理员账号。" />
    <a-alert v-if="importError" type="error" show-icon class="page-alert" :message="importError" />
    <a-alert v-if="editorDirty" type="warning" show-icon class="page-alert" message="校对内容已修改，请先点击“重新识别”，再确认导入。" />
    <a-alert v-else-if="importedSnapshot" type="info" show-icon class="page-alert" :message="importNotice" />
    <label v-if="missingAnswerCount > 0" class="import-skip-choice">
      <input type="checkbox" :checked="skipMissingAnswers" :disabled="!canImport() || loading || submitLoading" :onChange="pageEntry.bind(setSkipMissingAnswers)" />
      <span>仅导入有答案字段的题目，未导入题目保留</span>
    </label>
    <main class="import-workspace">
    <ConsolePanel class="compileBox" title="题目编辑" description="上传 .docx 文档后，按题目格式校对识别内容。">
      <template #actions>
          <a-upload name="file" action="/sqp/api/upload" :onChange="pageEntry.bind(handleChange)" :showUploadList="false"
            :beforeUpload="pageEntry.bind(beforeUpload)" :disabled="!canImport() || submitLoading" accept=".docx">
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
          <div class="compileContent" :key="'compile:' + paperKey" ref="compileContent" :onScroll="pageEntry.bind(handleScroll)" :onInput="pageEntry.bind(handleEditorInput)">
            <div v-for="(i, index) in showProblemArr" :key="index" :contenteditable="canImport() && !submitLoading ? 'true' : 'false'" role="textbox" aria-multiline="true" :aria-label="'第 ' + (index + 1) + ' 题校对内容'"
              :class="type == '全部' ? '' : type == i.typeAlias ? '' : 'display'">
              <p class="line" v-if="i.typeAlias"> 【{{ i.typeAlias }}】</p>
              <p class="lines" v-if="i.content">{{ index + 1 }}. {{ i.content }} </p>
              <template v-if="i.options">
                <p class="lines" v-for="(val, key) in i.options" :key="key">
                  {{ val.name ?? String.fromCharCode(65 + key) }}.{{ val.value }}
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
      <a-button :onClick="pageEntry.bind(handleInput)" :disabled="!canImport() || submitLoading" type="primary">
        重新识别
      </a-button>
      <a-button :onClick="pageEntry.bind(clearQuestions)" :disabled="!canImport() || submitLoading">
        清空题目
      </a-button>
    </div>
    <ConsolePanel class="previewBox" title="题目预览" description="检查题型、选项和答案，确认后导入题库。">
      <template #actions><a-tag v-if="data.length > 0" color="cyan">共识别 {{ data.length }} 题</a-tag></template>
      <div v-if="problemArr.length" class="tabs" role="group" aria-label="预览题型筛选">
        <button v-for="(i, index) in problemArr" :key="index" type="button" :aria-pressed="type === i.typeAlias" :onClick="pageEntry.bind(setType, i.typeAlias)"
          :class="type === i.typeAlias ? 'active tabsBtn' : 'tabsBtn'">
          {{ i.typeAlias || '其他' }}({{ i.paperList.length }})
        </button>
      </div>
      <a-spin :spinning="loading">
        <template v-if="showProblemArr.length > 0">
          <div class="previewContent" :key="'preview:' + paperKey" ref="previewContent" :onScroll="pageEntry.bind(handleScroll)">
            <div v-for="(i, index) in showProblemArr" :key="index"
              :class="type === '全部' ? '' : type === i.typeAlias ? '' : 'display'">
              <p class="line" v-if="i.typeAlias"> 【{{ i.typeAlias }}】</p>
              <p class="lines" v-if="i.content">{{ index + 1 }}. {{ i.content }} </p>
              <template v-if="i.options">
                <p class="lines" v-for="(val, key) in i.options" :key="key">
                  {{ val.name ?? String.fromCharCode(65 + key) }}.{{ val.value }}
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
import { message, Modal } from 'ant-design-vue';
import { onBeforeRouteLeave, useRouter } from 'vue-router';
import { UploadOutlined } from '@ant-design/icons-vue';
import { computed, defineComponent, nextTick, onBeforeUnmount, reactive, ref, shallowRef, watch } from 'vue';
import { parseFile, reParseFile } from '@/api/api';
import { createQuestions } from '@/api/TikuComponentApi';
import { getQuestionTypeByName } from '@/utils/uitls';
import { isAdminUser, useCurrentUserDisplay } from '@/utils/currentUser';
import { useUnsavedDraft } from '@/utils/useUnsavedDraft';

export default defineComponent({
  name: 'IntelligentImport',
  components: { UploadOutlined },
  setup() {
    const router = useRouter();
    const { currentUser } = useCurrentUserDisplay();
    const UploadId = ref(''), problemArr = ref([]), showProblemArr = ref([]);
    const loading = ref(false), data = ref([]), type = ref('全部'), submitLoading = ref(false);
    const importError = ref(''), editorDirty = ref(false), domRevision = ref(0), importedSnapshot = ref('');
    const parseReady = ref(false), importNotice = ref(''), skipMissingAnswers = ref(false);
    const missingAnswerCount = computed(() => data.value.filter(item => item.answer === undefined).length);
    let lastImportedPayload = '';
    const compileContent = ref(null), previewContent = ref(null), pageSurface = ref(null);
    const pageMounted = ref(true), pageKey = ref(0), paperKey = ref(0);
    let alive = true, epoch = 0, parseVersion = 0, importVersion = 0, renderVersion = 0;
    let activeUpload = null, busyOwner = null, discardPrompt = null;
    const principal = () => {
      const id = currentUser.value?.id;
      if (typeof id !== 'number' && (typeof id !== 'string' || !/^[0-9]+$/.test(id.trim()))) return '';
      const value = Number(id);
      return Number.isSafeInteger(value) && value > 0 && value <= 2147483647 ? 'id:' + value : '';
    };
    const role = () => String(currentUser.value?.role || '').trim().toLowerCase();
    const canImport = () => alive && pageMounted.value && Boolean(principal()) && isAdminUser(currentUser.value);
    const captureOwner = () => {
      const ticket = epoch, identity = principal(), privilege = role();
      return () => alive && ticket === epoch && identity === principal() && privilege === role() && canImport();
    };
    const makeEntry = () => {
      const owns = captureOwner(), version = renderVersion;
      const current = () => owns() && version === renderVersion;
      return {
        guardNative(event) {
          if (current()) return;
          event.preventDefault(); event.stopImmediatePropagation();
        },
        bind: (fn, ...args) => (...events) => current() ? fn(...args, ...events) : false,
      };
    };
    const pageEntry = shallowRef(makeEntry());
    const renewEntry = () => { renderVersion += 1; pageEntry.value = makeEntry(); };
    const draftState = reactive({
      paper: computed(() => data.value), file: computed(() => UploadId.value), dom: computed(() => domRevision.value),
      skipMissing: computed(() => skipMissingAnswers.value),
    });
    const { currentConfigSnapshot, savedConfigSnapshot, hasUnsavedChanges, requestRouteLeave: requestDraftLeave } =
      useUnsavedDraft(draftState, { focusTriggerAfterClose: false });
    const closeDiscardPrompt = () => {
      const prompt = discardPrompt;
      if (!prompt) return;
      discardPrompt = null; prompt.resolve(false); prompt.modal?.destroy();
    };
    const confirmDiscard = (owns, action) => {
      if (!owns()) return Promise.resolve(false);
      if (!hasUnsavedChanges.value) return Promise.resolve(true);
      if (discardPrompt) return discardPrompt.promise;
      const prompt = { resolve: null, modal: null, promise: null };
      prompt.promise = new Promise(resolve => { prompt.resolve = resolve; });
      discardPrompt = prompt;
      const settle = allow => {
        if (discardPrompt !== prompt) return;
        discardPrompt = null; prompt.resolve(Boolean(allow && owns())); prompt.modal?.destroy();
      };
      prompt.modal = Modal.confirm({
        title: '有未保存的更改，确认' + action + '？', content: '此操作将放弃当前题目及未重新识别的校对编辑。',
        okText: '放弃更改', cancelText: '保留编辑', autoFocusButton: 'cancel', focusTriggerAfterClose: false,
        onOk: () => settle(true), onCancel: () => settle(false),
      });
      return prompt.promise;
    };
    const requestRouteLeave = async to => {
      const ticket = epoch;
      const allow = await requestDraftLeave(to);
      if (!allow || !alive || ticket !== epoch) return false;
      await nextTick(); await nextTick();
      return Boolean(alive && ticket === epoch);
    };
    onBeforeRouteLeave(requestRouteLeave);
    const navigateToHome = () => { if (canImport()) return router.push('/adapter/component'); };
    const finishBusy = token => {
      if (busyOwner === token) { busyOwner = null; loading.value = false; }
    };
    const reportFailure = text => { importError.value = text; message.error(text); };
    const validCRC = value => (typeof value === 'string' && value.trim().length > 0 && value.length <= 256 && [...value].every(character => character.charCodeAt(0) >= 32)) ||
      (typeof value === 'number' && Number.isFinite(value));
    const finiteScalar = value => typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value));
    // Explicit parser fields only: ownership and database identifiers are never copied.
    const normalizePaper = paper => {
      if (!Array.isArray(paper)) throw new Error('invalid paper');
      return paper.map(item => {
        if (!item || typeof item !== 'object' || Array.isArray(item) ||
          (item.content !== undefined && typeof item.content !== 'string') ||
          (item.typeAlias !== undefined && typeof item.typeAlias !== 'string') ||
          (item.hintMsg !== undefined && typeof item.hintMsg !== 'string')) throw new Error('invalid question');
        const question = {};
        for (const field of ['content', 'typeAlias', 'hintMsg', 'course_name', 'source_name', 'extra']) {
          if (item[field] !== undefined) {
            if (typeof item[field] !== 'string') throw new Error('invalid question field');
            question[field] = item[field];
          }
        }
        if (item.options !== undefined) {
          if (!Array.isArray(item.options)) throw new Error('invalid options');
          question.options = item.options.map(option => {
            if (!option || typeof option !== 'object' || (option.name !== undefined && typeof option.name !== 'string') || !finiteScalar(option.value)) throw new Error('invalid option');
            return option.name === undefined ? { value: option.value } : { name: option.name, value: option.value };
          });
        }
        if (item.answer !== undefined) {
          if (!Array.isArray(item.answer) || item.answer.some(answer => !finiteScalar(answer))) throw new Error('invalid answer');
          question.answer = [...item.answer];
        }
        return question;
      });
    };
    const erasePaperPixels = () => {
      for (const surface of [compileContent.value, previewContent.value]) {
        if (!surface) continue;
        for (const node of surface.querySelectorAll('[contenteditable], .line, .lines')) node.textContent = '';
        surface.textContent = '';
      }
    };
    const applyPaper = paper => {
      // Vue cannot reconcile user-mutated contenteditable DOM from its old VDOM.
      // Rebuild only after an accepted parse; failed reads keep the actual edits.
      erasePaperPixels(); paperKey.value += 1;
      showProblemArr.value = paper; data.value = paper;
      problemArr.value = paper.length ? [
        { typeAlias: '全部', paperList: paper },
        ...[...new Set(paper.map(item => item.typeAlias))].map(alias => ({
          typeAlias: alias, paperList: paper.filter(item => item.typeAlias === alias),
        })),
      ] : [];
      type.value = '全部'; editorDirty.value = false; domRevision.value = 0; skipMissingAnswers.value = false;
      importedSnapshot.value = ''; importError.value = ''; parseReady.value = paper.length > 0;
      renewEntry();
    };
    const processPaperData = paper => {
      if (!canImport() || submitLoading.value) return;
      applyPaper(normalizePaper(paper));
    };
    const runParse = async (request, failureText, uploadToken = null) => {
      if (!canImport() || submitLoading.value) return;
      const owns = captureOwner(), ticket = ++parseVersion, snapshot = currentConfigSnapshot.value;
      const token = { owns, ticket };
      busyOwner = token; loading.value = true; importError.value = ''; parseReady.value = false;
      try {
        if (!owns()) return;
        const res = await request();
        if (!owns() || ticket !== parseVersion || snapshot !== currentConfigSnapshot.value) return;
        if (res?.status !== undefined && res.status !== 200) throw new Error('parse rejected');
        const paper = normalizePaper(res?.paper);
        applyPaper(paper);
        if (uploadToken) UploadId.value = uploadToken.crc;
        if (!paper.length) { importError.value = '识别完成，但未识别到题目；可检查文件格式后重试。'; message.info(importError.value); }
      } catch {
        if (owns() && ticket === parseVersion && snapshot === currentConfigSnapshot.value) reportFailure(failureText);
      } finally {
        if (owns() && ticket === parseVersion) finishBusy(token);
      }
    };
    const getParseFile = uploadToken => {
      if (!canImport() || submitLoading.value) return;
      const crc = uploadToken?.crc ?? UploadId.value;
      if (!validCRC(crc)) { reportFailure('上传结果无效，请重新上传文件。'); return; }
      return runParse(() => parseFile({ file: crc }), '识别文件失败，当前题目与校对编辑仍保留，请重试。', uploadToken);
    };
    const reParse = html => {
      if (!canImport() || submitLoading.value || typeof html !== 'string') return;
      return runParse(() => reParseFile({ html }), '重新识别失败，当前题目与校对编辑仍保留，请重试。');
    };
    const acceptUpload = (file, owns) => {
      if (!owns() || submitLoading.value) return false;
      const token = { file, uid: file.uid, owns, settled: false, crc: null, snapshot: currentConfigSnapshot.value };
      activeUpload = token; parseVersion += 1; lastImportedPayload = ''; importedSnapshot.value = '';
      busyOwner = token; loading.value = true; importError.value = ''; parseReady.value = false;
      message.info('文件开始上传，请耐心等待…');
      return true;
    };
    const beforeUpload = file => {
      if (!canImport() || submitLoading.value || !file || typeof file !== 'object') return false;
      if (typeof file.name !== 'string' || !/\.docx$/i.test(file.name) ||
        (file.size !== undefined && (typeof file.size !== 'number' || !Number.isFinite(file.size) || file.size < 0 || file.size > 16 * 1024 * 1024))) {
        reportFailure('仅支持上传不超过 16 MiB 的 .docx 文件。'); return false;
      }
      if (activeUpload && (activeUpload.file === file || (file.uid !== undefined && file.uid === activeUpload.uid))) return false;
      const owns = captureOwner();
      if (!hasUnsavedChanges.value) return acceptUpload(file, owns);
      return confirmDiscard(owns, '替换上传文件').then(allow => allow ? acceptUpload(file, owns) : false);
    };
    const handleChange = info => {
      const token = activeUpload, file = info?.file;
      if (!canImport() || !token || !token.owns() || token.settled || !file ||
        !(file === token.file || (token.uid !== undefined && file.uid === token.uid))) return;
      if (file.status === 'uploading') return;
      if (file.status !== 'done' && file.status !== 'error' && file.status !== 'removed') return;
      token.settled = true;
      if (file.status !== 'done') {
        finishBusy(token); reportFailure('文件上传失败，当前题目与校对编辑仍保留，请重新上传。'); return;
      }
      if (!validCRC(file.response?.crc)) {
        finishBusy(token); reportFailure('上传结果无效，当前题目与校对编辑仍保留，请重新上传。'); return;
      }
      if (token.snapshot !== currentConfigSnapshot.value) {
        finishBusy(token); message.warning('上传期间校对内容已修改，已保留当前编辑，请先重新识别或重新上传。'); return;
      }
      token.crc = file.response.crc;
      return getParseFile(token);
    };
    const handleEditorInput = event => {
      if (!canImport() || submitLoading.value || !compileContent.value?.contains(event?.target)) return;
      domRevision.value += 1; editorDirty.value = true; importedSnapshot.value = '';
    };
    const handleInput = () => {
      if (!canImport() || submitLoading.value || !showProblemArr.value.length || !compileContent.value) return;
      const html = Array.from(compileContent.value.children).map(editor => {
        const copy = editor.cloneNode(true);
        // Parser hint messages are not question text; all actual user blocks are.
        for (const hint of copy.querySelectorAll('.line.red')) hint.remove();
        return Array.from(copy.childNodes).map(node => {
          if (node.nodeType === 3) {
            if (!node.textContent.trim()) return '';
            const paragraph = document.createElement('p');
            paragraph.textContent = node.textContent;
            return paragraph.outerHTML;
          }
          if (node.nodeType !== 1) return '';
          if (node.tagName === 'P') return '<p>' + node.innerHTML + '</p>';
          // New blocks contribute text, never their event/src/form attributes.
          const plain = node.cloneNode(true);
          for (const active of plain.querySelectorAll('script, style, template, iframe, object, embed, input, button, select, textarea')) active.remove();
          for (const br of plain.querySelectorAll('br')) br.replaceWith(document.createTextNode('\n'));
          for (const block of plain.querySelectorAll('div, p, li, blockquote, pre')) block.appendChild(document.createTextNode('\n'));
          return plain.textContent.split(/\r\n|\r|\n/).map(line => {
            const paragraph = document.createElement('p');
            paragraph.textContent = line;
            return paragraph.outerHTML;
          }).join('');
        }).join('');
      }).join('');
      return reParse(html);
    };
    const clearState = () => {
      parseVersion += 1; activeUpload = null; busyOwner = null;
      UploadId.value = ''; loading.value = false; lastImportedPayload = ''; applyPaper([]);
      savedConfigSnapshot.value = currentConfigSnapshot.value;
    };
    const clearQuestions = async () => {
      if (!canImport() || submitLoading.value) return;
      const owns = captureOwner();
      if (!await confirmDiscard(owns, '清空题目') || !owns() || submitLoading.value) return;
      clearState();
    };
    const handleScroll = event => {
      if (!canImport()) return;
      const source = event?.target, editor = compileContent.value, preview = previewContent.value;
      if (!editor || !preview) return;
      if (source === editor) preview.scrollTop = source.scrollTop;
      else if (source === preview) editor.scrollTop = source.scrollTop;
    };
    const setType = alias => {
      if (canImport() && problemArr.value.some(item => item.typeAlias === alias)) type.value = alias;
    };
    const setSkipMissingAnswers = event => {
      if (!canImport() || loading.value || submitLoading.value || !missingAnswerCount.value) return;
      skipMissingAnswers.value = Boolean(event?.target?.checked); importError.value = '';
    };
    const upload = async () => {
      if (!canImport() || submitLoading.value || loading.value) return;
      if (editorDirty.value) { message.warning('校对内容已修改，请先点击“重新识别”，再确认导入。'); return; }
      const owns = captureOwner(), snapshot = currentConfigSnapshot.value;
      if (importedSnapshot.value === snapshot) { message.info('本次请求已处理，请核对结果，勿直接重复提交。'); return; }
      if (!parseReady.value) { message.warning('当前识别尚未成功，请先重新识别或重新上传文件，再确认导入。'); return; }
      let result;
      try {
        const paper = normalizePaper(data.value);
        if (!paper.length) { message.warning('当前没有可导入的题目，请先上传文件并检查答案。'); return; }
        // Only a missing answer field may be skipped, and only by explicit choice.
        // Empty question text and malformed existing fields still reject the batch.
        if (paper.some(item => typeof item.content !== 'string' || !item.content.trim())) throw new Error('incomplete question');
        if (paper.some(item => item.answer === undefined) && !skipMissingAnswers.value) {
          reportFailure('存在缺少答案字段的题目，请先校对；或明确勾选“仅导入有答案字段的题目，未导入题目保留”。'); return;
        }
        const selected = skipMissingAnswers.value ? paper.filter(item => item.answer !== undefined) : paper;
        if (!selected.length) { message.warning('当前没有含答案字段的可导入题目，未导入题目仍保留，请先校对。'); return; }
        result = selected.map(item => {
          if (!Array.isArray(item.answer) ||
            item.answer.some(answer => typeof answer === 'string' && !answer.trim())) throw new Error('incomplete question');
          const question = { question: item.content, type: getQuestionTypeByName(item.typeAlias) };
          const options = item.options?.map(option => option.value);
          let answer = item.answer;
          if (options?.length) {
            answer = item.answer.map(selector => {
              if (typeof selector !== 'string' || !/^[A-Z]$/.test(selector)) throw new Error('invalid answer selector');
              const index = selector.charCodeAt(0) - 65;
              if (index >= options.length) throw new Error('invalid answer index');
              return options[index];
            });
          }
          question.answer = JSON.stringify(answer);
          question.options = JSON.stringify(options);
          for (const field of ['course_name', 'source_name', 'extra']) if (item[field] !== undefined) question[field] = item[field];
          return question;
        });
      } catch {
        reportFailure('题目、选项或答案不完整，请校对全部题目并重新识别后再导入。'); return;
      }
      const payloadSnapshot = JSON.stringify(result);
      if (lastImportedPayload === payloadSnapshot) {
        importedSnapshot.value = snapshot; message.info('本次请求已处理，请核对结果，勿直接重复提交。'); return;
      }
      const ticket = ++importVersion;
      submitLoading.value = true; importError.value = '';
      try {
        if (!owns()) return;
        const res = await createQuestions(result);
        if (!owns() || ticket !== importVersion) return;
        if (res?.status !== 200) throw new Error('import rejected');
        importedSnapshot.value = snapshot; lastImportedPayload = payloadSnapshot;
        const match = typeof res.message === 'string' && /^成功创建([0-9]+)条数据$/.exec(res.message);
        const created = match ? Number(match[1]) : NaN;
        const fullyCreated = created === result.length && result.length === data.value.length;
        if (fullyCreated && currentConfigSnapshot.value === snapshot) savedConfigSnapshot.value = snapshot;
        else savedConfigSnapshot.value = null;
        if (Number.isSafeInteger(created) && created >= 0 && created <= result.length) {
          importNotice.value = '本次提交 ' + result.length + ' 题，实际创建 ' + created + ' 题；请核对结果，勿重复提交本次题目。';
          if (created === result.length) message.success(importNotice.value);
          else message.warning(importNotice.value);
        } else {
          importNotice.value = '导入请求已处理，本次提交 ' + result.length + ' 题；请核对题库结果，勿重复提交本次题目。';
          message.info(importNotice.value);
        }
      } catch (error) {
        if (owns() && ticket === importVersion) reportFailure(error?.code === 'UNKNOWN_OUTCOME'
          ? '导入结果未确认，当前题目仍保留；请先核对题库，勿直接重复提交。'
          : '导入失败，当前题目仍保留，请核对结果后重试。');
      } finally {
        if (owns() && ticket === importVersion) submitLoading.value = false;
      }
    };
    const retireScope = () => {
      epoch += 1; parseVersion += 1; importVersion += 1;
      closeDiscardPrompt(); requestDraftLeave({ path: '/login' });
      // Erase private pixels synchronously; never return focus to a retired actor.
      const surface = pageSurface.value;
      if (surface) {
        surface.hidden = true; surface.style.display = 'none';
        if (surface.contains(document.activeElement)) document.activeElement.blur?.();
        for (const input of surface.querySelectorAll('input, textarea')) input.value = '';
        erasePaperPixels();
      }
      pageMounted.value = false;
      for (const row of data.value) {
        for (const field of ['content', 'typeAlias', 'hintMsg', 'course_name', 'source_name', 'extra']) if (field in row) row[field] = '';
        for (const option of row.options || []) { option.name = ''; option.value = ''; }
        if (row.options) row.options.length = 0;
        if (row.answer) { row.answer.fill(''); row.answer.length = 0; }
      }
      lastImportedPayload = ''; parseReady.value = false; importNotice.value = ''; skipMissingAnswers.value = false;
      activeUpload = null; busyOwner = null; loading.value = false; submitLoading.value = false;
      UploadId.value = ''; problemArr.value = []; showProblemArr.value = []; data.value = [];
      type.value = '全部'; domRevision.value = 0; editorDirty.value = false; importError.value = ''; importedSnapshot.value = '';
      savedConfigSnapshot.value = currentConfigSnapshot.value;
      renewEntry();
      const ticket = epoch;
      nextTick().then(() => {
        if (!alive || ticket !== epoch) return;
        pageKey.value += 1; pageMounted.value = true; renewEntry();
      });
    };
    watch(() => [principal(), role()], (next, previous) => {
      if (next[0] !== previous[0] || next[1] !== previous[1]) retireScope();
    }, { flush: 'sync' });
    onBeforeUnmount(() => { alive = false; retireScope(); });
    return {
      UploadId, problemArr, showProblemArr, loading, data, type, submitLoading,
      importError, editorDirty, importedSnapshot, parseReady, importNotice, skipMissingAnswers, missingAnswerCount, setSkipMissingAnswers, compileContent, previewContent,
      pageSurface, pageMounted, pageKey, paperKey, pageEntry, canImport, hasUnsavedChanges, requestRouteLeave,
      navigateToHome, getParseFile, reParseFile: reParse, processPaperData, handleChange, beforeUpload,
      handleScroll, handleEditorInput, handleInput, clearQuestions, setType, upload,
    };
  },
});
</script>
<style scoped>
.page-alert { margin-bottom: 16px; }
.import-skip-choice { display: flex; align-items: center; gap: 8px; margin-bottom: 16px; color: var(--console-muted); font-size: 13px; line-height: 1.7; }
.import-skip-choice input { accent-color: var(--console-primary); }
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
