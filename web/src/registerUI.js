import { Tabs, Pagination, Table, Tag, Modal, InputNumber, Select, AutoComplete, Checkbox, Switch, Layout, Row, Col, Space, Upload, Menu, Popconfirm, Badge, Empty, Statistic } from 'ant-design-vue'
import { registerPublicUI } from './publicUI'
import ConsolePageHeading from './components/ConsolePageHeading.vue'
import ConsolePanel from './components/ConsolePanel.vue'
import ConsoleSummaryGrid from './components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from './components/ConsoleTableShell.vue'

export function registerConsoleExtras(app) {
  for (const widget of [Tabs, Pagination, Table, Tag, Modal, InputNumber, Select, AutoComplete, Checkbox, Switch, Layout, Row, Col, Space, Upload, Menu, Popconfirm, Badge, Empty, Statistic]) app.use(widget)
  for (const widget of [ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell]) app.component(widget.name, widget)
  return app
}

// Standalone console mounts use exactly the union installed by the production router.
export function registerConsoleUI(app) {
  return registerConsoleExtras(registerPublicUI(app))
}
