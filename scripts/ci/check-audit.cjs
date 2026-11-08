const fs = require('node:fs')
const levels = ['info', 'low', 'moderate', 'high', 'critical']
function evaluateAudit(report, policy, npmExit) {
  if (!report || report.error || report.auditReportVersion !== 2
      || !report.vulnerabilities || Array.isArray(report.vulnerabilities)
      || !report.metadata?.vulnerabilities) throw new Error('Expected a valid audit report v2')
  if (!policy || policy.schemaVersion !== 1 || !levels.includes(policy.failAt)) throw new Error('Invalid audit policy')
  if (!Array.isArray(policy.exceptions) || policy.exceptions.length) throw new Error('Audit exception requires separate approved policy implementation; none are currently accepted')
  const counts = report.metadata.vulnerabilities
  const actual = Object.fromEntries(levels.map(x => [x, 0]))
  const packages = Object.entries(report.vulnerabilities).map(([name, entry]) => {
    if (!entry || !levels.includes(entry.severity)) throw new Error('Unknown audit severity')
    actual[entry.severity] += 1
    return { package: name, severity: entry.severity }
  })
  const total = packages.length
  for (const key of levels) {
    if (!Number.isInteger(counts[key]) || counts[key] < 0 || counts[key] !== actual[key]) throw new Error('Inconsistent audit count')
  }
  if (!Number.isInteger(counts.total) || counts.total !== total) throw new Error('Inconsistent audit total count')
  if (![0, 1].includes(npmExit) || (npmExit === 0) !== (total === 0)) throw new Error('Unexpected npm exit status for audit result')
  const blocked = packages.filter(x => levels.indexOf(x.severity) >= levels.indexOf(policy.failAt))
  return { decision: blocked.length ? 'block' : 'pass', counts: { ...counts }, threshold: policy.failAt, blocked }
}
if (require.main === module) {
  try {
    const args = process.argv.slice(2)
    if (args.length !== 5 || args[1] !== '--npm-exit' || args[3] !== '--policy' || !/^\d+$/.test(args[2])) throw new Error('Usage: check-audit.cjs REPORT --npm-exit STATUS --policy POLICY')
    const result = evaluateAudit(JSON.parse(fs.readFileSync(args[0], 'utf8')), JSON.parse(fs.readFileSync(args[4], 'utf8')), Number(args[2]))
    console.log(JSON.stringify(result, null, 2))
    process.exitCode = result.decision === 'pass' ? 0 : 1
  } catch (error) {
    console.error('Audit gate error: ' + error.message)
    process.exitCode = 2
  }
}
module.exports = { evaluateAudit }
