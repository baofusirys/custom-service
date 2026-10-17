<script setup>
// [122] 敏感词检测试用：按当前页面上的规则（含未保存的修改）调用 /api/admin/hall/filter/test 预览屏蔽结果。
// 结果一律文本插值渲染（无 v-html）；接口每管理员 30 次/分钟，超限由 hallErrors 映射为中文提示。
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { testHallFilter } from '../../api/hallFilter'
import { hallErrorMessage } from '../../modules/hallErrors'
import { contentLines } from '../../modules/hallMessages'
import { TEST_CONTENT_MAX, categoryLabel, parseTestResult, validateTestContent } from '../../modules/hallFilterForm'

const props = defineProps({
  // () => { ok, body } | { ok: false, error }：当前表单转成的规则（结构同保存接口）
  buildConfig: { type: Function, required: true },
  disabled: { type: Boolean, default: false },
})

const content = ref('')
const testing = ref(false)
const result = ref(null)

async function run() {
  if (testing.value || props.disabled) return
  const check = validateTestContent(content.value)
  if (!check.ok) {
    ElMessage.warning(check.error)
    return
  }
  const config = props.buildConfig()
  if (!config || !config.ok) {
    ElMessage.warning('当前规则有误，请先修正：' + (config?.error || '规则无效'))
    return
  }
  testing.value = true
  try {
    const r = await testHallFilter({ content: content.value, config: config.body })
    result.value = parseTestResult(r?.data)
  } catch (e) {
    ElMessage.error(hallErrorMessage(e, '检测失败，请稍后重试'))
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <el-form label-width="120px" label-position="left" @submit.prevent>
    <el-form-item label="试用文字">
      <el-input
        v-model="content"
        type="textarea"
        :rows="3"
        :maxlength="TEST_CONTENT_MAX"
        show-word-limit
        placeholder="输入一段文字，按当前页面上的规则（含未保存的修改）检测，不会发到大厅"
        data-testid="hall-filter-test-input" />
    </el-form-item>
    <el-form-item>
      <el-button type="primary" plain :loading="testing" :disabled="disabled" data-testid="hall-filter-test-run" @click="run">检测</el-button>
    </el-form-item>
  </el-form>

  <el-descriptions v-if="result" :column="1" border size="small" data-testid="hall-filter-test-result">
    <el-descriptions-item label="是否命中">
      <el-tag :type="result.filtered ? 'danger' : 'success'" data-testid="hall-filter-test-verdict">{{ result.filtered ? '命中，发出后会被屏蔽' : '未命中，原样发出' }}</el-tag>
    </el-descriptions-item>
    <el-descriptions-item label="命中类别">
      <el-space v-if="result.categories.length" wrap :size="6">
        <el-tag v-for="c in result.categories" :key="c" type="warning" size="small">{{ categoryLabel(c) }}</el-tag>
      </el-space>
      <el-text v-else size="small" type="info">无</el-text>
    </el-descriptions-item>
    <el-descriptions-item v-if="result.hits.length" label="命中片段">
      <el-space wrap :size="6">
        <el-tag v-for="(h, i) in result.hits" :key="i" type="info" size="small" effect="plain">{{ categoryLabel(h.category) }}：{{ h.text }}</el-tag>
      </el-space>
    </el-descriptions-item>
    <el-descriptions-item label="屏蔽后结果">
      <el-text data-testid="hall-filter-test-masked"><template v-for="(line, i) in contentLines(result.masked)" :key="i"><br v-if="i" />{{ line }}</template></el-text>
    </el-descriptions-item>
  </el-descriptions>
</template>
