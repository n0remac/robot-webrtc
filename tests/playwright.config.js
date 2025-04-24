const { devices } = require('playwright');

module.exports = {
    projects: [
        {
            name: 'chromium',
            use: {
                ...devices['Pixel 5'],
                launchOptions: {
                    args: ['--disable-web-security',
                        '--use-fake-ui-for-media-stream',
                        '--use-fake-device-for-media-stream'
                    ],
                }
            },
        },
    ],
};