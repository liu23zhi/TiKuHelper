import {
  Tabs, Alert, Button, Pagination, Table, Tag, Modal, Form, Input, InputNumber,
  Select, AutoComplete, Checkbox, Switch, Layout, Row, Col, Tooltip, Card,
  Space, Upload, Spin, Menu, Popconfirm, Badge, Empty, Radio, Statistic,
  ConfigProvider, Popover, Descriptions, Drawer, Timeline, Divider, Result, Typography,
} from 'ant-design-vue'
import ConsolePageHeading from './components/ConsolePageHeading.vue'
import ConsolePanel from './components/ConsolePanel.vue'
import ConsoleSummaryGrid from './components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from './components/ConsoleTableShell.vue'

// Used by the application and runtime tests. Missing registration must fail the
// same way in tests as in production (a full Antd plugin would conceal it).
export function registerConsoleUI(app) {
  for (const component of [Tabs, Alert, Button, Pagination, Table, Tag, Modal, Form, Input, InputNumber, Select, AutoComplete, Checkbox, Switch, Layout, Row, Col, Tooltip, Card, Space, Upload, Spin, Menu, Popconfirm, Badge, Empty, Radio, Statistic, ConfigProvider, Popover, Descriptions, Drawer, Timeline, Divider, Result, Typography]) app.use(component)
  for (const component of [ConsolePageHeading, ConsolePanel, ConsoleSummaryGrid, ConsoleTableShell]) app.component(component.name, component)
  return app
}
