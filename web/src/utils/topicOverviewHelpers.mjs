export function buildQuestionBulkActionIds(values = []) {
  const seen = new Set();
  const ids = [];

  for (const value of values || []) {
    const id = Number(value);
    if (!Number.isInteger(id) || id <= 0 || seen.has(id)) {
      continue;
    }
    seen.add(id);
    ids.push(id);
  }

  return ids;
}

export function buildQuestionBulkCourseUpdatePayload(values = [], courseName = '') {
  const ids = buildQuestionBulkActionIds(values);
  if (!ids.length) {
    throw new Error('请先选择需要批量修改的题目');
  }

  const normalizedCourseName = String(courseName || '').trim();
  if (!normalizedCourseName) {
    throw new Error('请输入课程名称');
  }

  return {
    ids,
    courseName: normalizedCourseName,
  };
}

export function buildQuestionCourseNameCandidates(keyword = '', options = []) {
  const normalizedKeyword = String(keyword || '').trim().toLowerCase();
  if (!normalizedKeyword) {
    return [];
  }

  const seen = new Set();
  const items = [];
  for (const option of Array.isArray(options) ? options : []) {
    const value = String(option?.value ?? option?.label ?? '').trim();
    if (!value) {
      continue;
    }
    const lowerValue = value.toLowerCase();
    if (!lowerValue.includes(normalizedKeyword) || seen.has(lowerValue)) {
      continue;
    }
    seen.add(lowerValue);
    items.push({ label: value, value });
  }
  return items;
}

export function buildQuestionExportPayload(rows = []) {
  const items = Array.isArray(rows) ? rows : [];
  return {
    filename: 'questions-export.json',
    content: JSON.stringify(items, null, 2),
  };
}

export function buildQuestionSourceLabel(sourceName) {
  const trimmed = String(sourceName || '').trim();
  return trimmed || '人工导入';
}

export function parseQuestionImportText(content) {
  try {
    const parsed = JSON.parse(String(content || ''));
    if (!Array.isArray(parsed)) {
      throw new Error('JSON 文件内容必须是题目数组');
    }
    return parsed;
  } catch (error) {
    if (error?.message === 'JSON 文件内容必须是题目数组') {
      throw error;
    }
    throw new Error('JSON 文件解析失败');
  }
}

export function buildQuestionImportDryRunPayload(items = []) {
  return {
    dryRun: true,
    overwrite: false,
    items: Array.isArray(items) ? items : [],
  };
}

export function buildQuestionImportExecutePayload(items = [], overwrite = false) {
  return {
    overwrite: Boolean(overwrite),
    items: Array.isArray(items) ? items : [],
  };
}

export function buildQuestionImportSummary(result = {}) {
  const existingIds = Array.isArray(result?.existingIds) ? result.existingIds.filter((id) => Number.isInteger(Number(id))) : [];
  if (existingIds.length) {
    return `检测到 ${existingIds.length} 条题目已存在（ID: ${existingIds.join(', ')}）。选择“是”将直接覆盖，选择“否”只导入不存在的数据。`;
  }

  const created = Number(result?.created || 0);
  return `共准备导入 ${created} 条题目，未发现 ID 冲突。`;
}

export function buildQuestionSelectionSummary(selectedCount = 0, totalCount = 0) {
  const normalizedSelected = Math.max(0, Number(selectedCount) || 0);
  const normalizedTotal = Math.max(0, Number(totalCount) || 0);
  return `已选择 ${normalizedSelected} 项，共 ${normalizedTotal} 项`;
}
