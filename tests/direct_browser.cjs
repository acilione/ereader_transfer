'use strict';
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs/promises');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const base = process.env.PAGEPORT_URL || 'http://127.0.0.1:18092';

(async () => {
  const browser = await chromium.launch({headless:true, ...(process.env.PLAYWRIGHT_BROWSER ? {executablePath:process.env.PLAYWRIGHT_BROWSER} : {})});
  try {
    const page = await browser.newPage({viewport:{width:1440,height:1000}});
    const errors=[]; page.on('pageerror',e=>errors.push(e.message));
    await page.goto(base);
    await page.waitForFunction(()=>document.querySelector('#server-status-text').textContent==='Local server is running');
    assert.equal((await (await page.request.get(base+'/api/state')).json()).files.length,0,'Use isolated empty data');
    await page.locator('.receiver-setup summary').click();
    const bundle=await page.request.get(base+'/api/receiver-package');
    assert.equal(bundle.status(),200); assert.equal(bundle.headers()['content-type'],'application/zip');
    assert.equal((await bundle.body()).subarray(0,2).toString(),'PK');
    assert.equal((await page.request.get(base+'/api/receiver-guide')).status(),200);
    await page.locator('#pair-device').click();
    await page.waitForFunction(()=>document.querySelector('#paired-devices').textContent.includes('Download configuration'));
    const configLink = page.locator('#paired-devices a').first();
    const config = await (await page.request.get(base+await configLink.getAttribute('href'))).json();
    const headers={Authorization:'Bearer '+config.token};
    await page.locator('#file-input').setInputFiles([
      {name:'One.epub',mimeType:'application/epub+zip',buffer:Buffer.from('first transport fixture')},
      {name:'Two.pdf',mimeType:'application/pdf',buffer:Buffer.from('second transport fixture')},
      {name:'Three.epub',mimeType:'application/epub+zip',buffer:Buffer.from('unselected fixture')}
    ]);
    await page.waitForFunction(()=>document.querySelectorAll('.select-book').length===3);
    await page.locator('#dismiss-uploads').waitFor({state:'visible'});
    await page.locator('#dismiss-uploads').click();
    await page.locator('.select-book').nth(0).check();
    await page.locator('.select-book').nth(1).check();
    assert.equal(await page.locator('#send-books').textContent(),'Send selected (2)');
    await page.locator('#send-books').click();
    await page.waitForFunction(()=>document.querySelector('#transfer-list').textContent.includes('Queued'));
    const offer=await (await page.request.get(base+'/receiver/poll',{headers})).json();
    assert.equal(offer.job.files.length,2);
    let download=base+'/receiver/jobs/'+offer.job.id+'/files/'+offer.job.files[0].id;
    assert.equal((await page.request.get(download,{headers})).status(),403);
    let response=await page.request.post(base+'/receiver/jobs/'+offer.job.id+'/status',{headers,data:{status:'receiving',message:'Accepted on test receiver'}});
    assert.equal(response.status(),200);
    response=await page.request.get(download,{headers}); assert.equal(response.status(),200);
    assert.ok((await response.body()).length>0);
    await page.request.post(base+'/receiver/jobs/'+offer.job.id+'/status',{headers,data:{status:'received',message:'Test receiver verified files and scanned library.'}});
    await page.waitForFunction(()=>document.querySelector('#transfer-list').textContent.includes('Received · library scan completed'));
    const output=process.env.PAGEPORT_QA_DIR || '.qa/direct'; await fs.mkdir(output,{recursive:true});
    await page.screenshot({path:path.join(output,'desktop.png'),fullPage:true});
    for (const width of [390,320]) {
      await page.setViewportSize({width,height:900});
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'horizontal overflow at '+width);
      await page.screenshot({path:path.join(output,'mobile-'+width+'.png'),fullPage:true});
    }
    assert.deepEqual(errors,[]);
    console.log('PASS: pairing, configuration, multi-select send, acceptance gate, transfer status, and responsive UI.');
  } finally { await browser.close(); }
})().catch(error=>{console.error(error);process.exitCode=1;});
