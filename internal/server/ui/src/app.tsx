import { Route, Switch } from 'wouter'
import { Shell } from './components/Shell'
import { Overview } from './screens/Overview'
import { Sessions } from './screens/Sessions'
import { SessionDetail } from './screens/SessionDetail'
import { Tasks } from './screens/Tasks'
import { Projects } from './screens/Projects'
import { ProjectDetail } from './screens/ProjectDetail'
import { Playbooks } from './screens/Playbooks'
import { PlaybookDetail } from './screens/PlaybookDetail'
import { InboxScreen } from './screens/Inbox'
import { KnowledgeBase } from './screens/KB'
import { Memories } from './screens/Memories'
import { Workdirs } from './screens/Workdirs'
import { Trash } from './screens/Trash'
import { Settings } from './screens/Settings'
import { EmptyState } from './components/ui'

export function App() {
  return (
    <Shell>
      <Switch>
        <Route path="/" component={Overview} />
        <Route path="/sessions" component={Sessions} />
        <Route path="/session/:slug">{(p) => <SessionDetail slug={p.slug} />}</Route>
        <Route path="/tasks" component={Tasks} />
        <Route path="/projects" component={Projects} />
        <Route path="/project/:slug">{(p) => <ProjectDetail slug={p.slug} />}</Route>
        <Route path="/playbooks" component={Playbooks} />
        <Route path="/playbook/:slug">{(p) => <PlaybookDetail slug={p.slug} />}</Route>
        <Route path="/inbox" component={InboxScreen} />
        <Route path="/kb" component={KnowledgeBase} />
        <Route path="/memories" component={Memories} />
        <Route path="/workdirs" component={Workdirs} />
        <Route path="/settings" component={Settings} />
        <Route path="/trash" component={Trash} />
        <Route>
          <div className="page">
            <EmptyState title="Not found" hint="That route doesn't exist." />
          </div>
        </Route>
      </Switch>
    </Shell>
  )
}
