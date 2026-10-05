document.getElementById('snippet').textContent =
      '<script src="' + location.origin + '/widget/loader.js"\n' +
      '        data-cs-endpoint="' + location.origin.replace(/^http/, 'ws') + '"\n' +
      '        data-cs-site="default" defer><\/script>';
