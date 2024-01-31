import * as React from 'react';
import {useEffect, useState} from 'react';
import {
  DataList,
  DataListCell,
  DataListContent,
  DataListItem,
  DataListItemCells,
  DataListItemRow,
  DataListToggle,
  Label,
  Title,
} from '@patternfly/react-core';
import {DeploymentDTO, DeploymentResourceService, IdentifiedDependencyDTO} from "../../services/openapi";
import {EmptyTable} from '@app/EmptyTable/EmptyTable';
import {
  AttentionBellIcon,ContainerNodeIcon,
  IceCreamIcon,
  InProgressIcon, ListIcon, OkIcon, OutlinedAngryIcon, RedhatIcon, StickyNoteIcon, WarningTriangleIcon
} from "@patternfly/react-icons";
import {Link} from "react-router-dom";

const DeploymentList: React.FunctionComponent = () => {
  const [deployments, setDeployments] = useState(Array<DeploymentDTO>);
  const [error, setError] = useState(false);
  const [state, setState] = useState('');


  useEffect(() => {
    setState('loading');
    DeploymentResourceService.getApiDeployment().then()
      .then((res) => {
        console.log(res);
        setState('success');
        setDeployments(res);
      })
      .catch((err) => {
        console.error('Error:', err);
        setState('error');
        setError(err);
      });
  }, []);

  if (state === 'error')
    return (
      <h1>
        {error.toString()}
      </h1>
    );
  if (state === 'loading')
    return (
      <h1>Loading...</h1>
    )

  return (
    <React.Fragment>

      <DataList aria-label="Information">
      {deployments.map((deployment : DeploymentDTO, index) => (
          <DeploymentRow key={index} deployment={deployment}></DeploymentRow>
        ))}
        {deployments.length === 0 && <EmptyTable></EmptyTable>}
      </DataList>
    </React.Fragment>
  );
};

type DeploymentActionsType = {
  deployment: DeploymentDTO,
};

const DeploymentRow: React.FunctionComponent<DeploymentActionsType> = (initialBuild): JSX.Element => {

  const [imagesExpanded, setImagesExpanded] = React.useState(false);

  const toggleImages = () => {
    setImagesExpanded(!imagesExpanded);
  };

  const health = function (deployment: DeploymentDTO) {
    if (!deployment.analysisComplete) {
      return <Label color="blue" icon={<InProgressIcon />}>
        Image Analysis in Progress
      </Label>
    }
    let untrusted = 0
    let total = 0
    let available = 0
    deployment.images.map((i) => {total += i.totalDependencies; untrusted += i.untrustedDependencies; available += i.availableBuilds})
    const trusted = total - untrusted
    if (total == 0) {
      return <Label color="blue" icon={<StickyNoteIcon />}>
        No Java
      </Label>
    }
    return <>
      {untrusted > 0 && <Label color="red" icon={<WarningTriangleIcon />}>{untrusted} Untrusted Dependencies</Label>}
      {trusted > 0 && <Label color="green" icon={<OkIcon />}>{trusted} Rebuilt Dependencies</Label>}
      {available > 0 && <Label color="orange" icon={<ListIcon />}>{available} Available Rebuilt Dependencies</Label>}

    </>
  }
  const dependencyRow = function (dep : IdentifiedDependencyDTO) {

    return <DataListItem>
      <DataListItemRow>
        <DataListItemCells
          dataListCells={[
            <DataListCell isIcon key="icon">
              {dep.source === 'rebuilt' && <OkIcon color={"green"}></OkIcon>}
              {dep.source === 'redhat' && <RedhatIcon color={"red"}></RedhatIcon>}
              {(dep.source !== 'redhat' && dep.source != 'rebuilt') && <WarningTriangleIcon color={"orange"}></WarningTriangleIcon>}
            </DataListCell>,
            <DataListCell key="primary content">
              {dep.dependencyBuildIdentifier != undefined && <Link to={`/builds/build/${dep.dependencyBuildIdentifier}`}>{dep.gav}</Link>}
              {dep.dependencyBuildIdentifier == undefined && <div id="gav">{dep.gav}</div>}
            </DataListCell>,
            <DataListCell key="primary content">
              {dep.inQueue && <Label color="blue" icon={<IceCreamIcon />}> In Build Queue</Label>}
              {(dep.buildAttemptId != null) && <Label color="green" icon={<OkIcon />}>Rebuilt Artifact</Label>}
              {(dep.buildAttemptId == null && dep.buildSuccess) && <Label color="orange" icon={<AttentionBellIcon />}>Rebuilt Artifact Available, Image Rebuild Required</Label>}
              {(dep.buildAttemptId == null && dep.dependencyBuildIdentifier != null && !dep.buildSuccess) && <Label color="red" icon={<OutlinedAngryIcon />}>Rebuild Failed</Label>}
              {(dep.buildAttemptId == null && dep.dependencyBuildIdentifier == null && !dep.buildSuccess) && <Label color="orange" icon={<OutlinedAngryIcon />}>Unknown Source</Label>}
            </DataListCell>,
          ]}
        />
      </DataListItemRow>
    </DataListItem>
  }

  return <DataListItem aria-labelledby="ex-item1" isExpanded={imagesExpanded}>
    <DataListItemRow>
      <DataListToggle
        onClick={() => toggleImages()}
        isExpanded={imagesExpanded}
        id="toggle"
        aria-controls="ex-expand"
      />
      <DataListItemCells
        dataListCells={[
          <DataListCell isIcon key="icon">
            <ContainerNodeIcon/>
          </DataListCell>,
          <DataListCell key="primary content">
            <div id="ex-item1">{initialBuild.deployment.namespace}/{initialBuild.deployment.name}</div>
          </DataListCell>,
          <DataListCell key="health">
            {health(initialBuild.deployment)}
          </DataListCell>
        ]}
      />
    </DataListItemRow>
    <DataListContent
      aria-label="First expandable content details"
      id="ex-expand1"
      isHidden={!imagesExpanded}
    >
      {initialBuild.deployment.images.map((s) => (
        <><Title headingLevel={"h2"}>Image: {s.fullName}</Title>

        <DataList aria-label="Dependencies">
          {s.dependencies?.map(d => (dependencyRow(d)))}
        </DataList>
        </>))}
    </DataListContent>
  </DataListItem>

}
export {DeploymentList};
