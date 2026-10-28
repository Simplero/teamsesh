# usage: configure.py home my_ts_ip peer_id peer_ts_ip folder_path folder_type
import sys, xml.etree.ElementTree as ET
home, ip, peer, peer_ip, path, ftype = sys.argv[1:]
cfg = home + '/config.xml'
t = ET.parse(cfg); r = t.getroot()
o = r.find('options')
for tag in ('listenAddress',): [o.remove(e) for e in o.findall(tag)]
ET.SubElement(o, 'listenAddress').text = f'tcp://{ip}:22100'
for tag, val in [('globalAnnounceEnabled','false'),('localAnnounceEnabled','false'),('relaysEnabled','false'),
                 ('natEnabled','false'),('autoUpgradeIntervalH','0'),('urAccepted','-1'),('crashReportingEnabled','false'),
                 ('startBrowser','false')]:
    e = o.find(tag)
    if e is None: e = ET.SubElement(o, tag)
    e.text = val
g = r.find('gui'); g.find('address').text = '127.0.0.1:28384'
me = r.find('device').get('id')
for f in r.findall('folder'): r.remove(f)
d = ET.SubElement(r, 'device', id=peer, name='peer', compression='metadata', introducer='false', autoAcceptFolders='false')
ET.SubElement(d, 'address').text = f'tcp://{peer_ip}:22100'
f = ET.Element('folder', id='teamsesh-calvin', label='teamsesh-calvin', path=path, type=ftype, rescanIntervalS='60', fsWatcherEnabled='true')
ET.SubElement(f, 'device', id=me); ET.SubElement(f, 'device', id=peer)
r.insert(0, f)
t.write(cfg)
print('ok', ftype)
