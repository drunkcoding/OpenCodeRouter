import * as path from 'node:path';

import Mocha from 'mocha';

export function run(): Promise<void> {
  const mocha = new Mocha({
    ui: 'bdd',
    color: true,
    timeout: 30_000
  });

  const testsRoot = path.resolve(__dirname);
  mocha.addFile(path.join(testsRoot, 'remoteHosts.ui.test.js'));

  return new Promise((resolve, reject) => {
    mocha.run((failures) => {
      if (failures > 0) {
        reject(new Error(`${failures} UI test(s) failed.`));
        return;
      }
      resolve();
    });
  });
}
