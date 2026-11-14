import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import assert from 'node:assert/strict'
for(const record of JSON.parse(readFileSync('provenance.json','utf8')).contracts) {
  assert.match(record.revision,/^[0-9a-f]{40}$/)
  const data=readFileSync('contracts/'+record.file,'utf8').replaceAll('\r\n','\n')
  assert.equal(createHash('sha256').update(data).digest('hex'),record.sha256,'Consumer artifact changed: '+record.file)
  assert.equal(JSON.parse(data).interactions.length,record.interactions)
}
